// Package main is the api-tuners Lambda. Operator-only BFF for the
// iter 9.4 tuner-delta review surface:
//
//	GET  /tuners?status=<s>            — PENDING TunerDeltas (default),
//	                                     newest-first via gsi1.
//	POST /tuners/{id}/apply  {ref}     — mutate the live profile from
//	                                     the proposal, bump its version,
//	                                     write a Feedback audit row,
//	                                     emit profile.updated; delta →
//	                                     applied.
//	POST /tuners/{id}/dismiss {ref,reason}
//	                                   — delta → dismissed + Feedback
//	                                     row. No profile change.
//
// No auto-apply (iter 9.4 is operator-gated). `ref` is base64("pk|sk")
// the list returns so a mutation can address the exact TunerDelta
// without a second lookup.
//
// Apply is wired for the kinds that have a canonical per-vertical
// profile: style (VerticalStyleGuide) and email_tone (EmailToneProfile)
// — both go through their pkg's Update, which bumps `version` (the
// spec/email cache key) atomically. targeting deltas are advisory at
// MVP: TargetingProfile is id-keyed (no per-vertical canonical row),
// so the delta is recorded + audited + evented but the operator wires
// keyword/weight nudges via /settings/targeting. Documented here per
// the spec-gap rule (.ralph is read-only).
//
// Synchronous operator BFF — auth.IsOperator gate, shared lambda_api
// role, no kill-switch / idempotency / cost-cap (not an event
// consumer; the only paid-ish work is DDB writes).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/auth"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/feedback"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/httpresp"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/style"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/tone"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/tunerlib"
)

const gsi1Index = "gsi1"

const (
	defaultLimit = 50
	maxLimit     = 100
)

var allowedStatuses = map[string]bool{"pending": true, "applied": true, "dismissed": true}

// errNoLongerPending is the conditional-check race: another request
// applied/dismissed this delta first. Surfaced to the caller as 409,
// not 500 (the action already succeeded — a retry would be wrong).
var errNoLongerPending = errors.New("tuner delta no longer pending")

// newPublisher is a seam so tests can supply a fake EventBridge.
var newPublisher = pkgevents.NewPublisher

type tunerItem struct {
	Ref       string          `json:"ref"`
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Vertical  string          `json:"vertical"`
	Status    string          `json:"status"`
	Rationale string          `json:"rationale"`
	Proposed  json.RawMessage `json:"proposed"`
	PromptID  string          `json:"promptId"`
	CreatedAt string          `json:"createdAt"`
}

type tunersResponse struct {
	Status string      `json:"status"`
	Items  []tunerItem `json:"items"`
}

func handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	logger := applog.FromContext(ctx)
	if !auth.IsOperator(req) {
		logger.Info("forbidden", "route", req.RouteKey)
		return httpresp.Error(403, "operator group required"), nil
	}
	method := strings.ToUpper(req.RequestContext.HTTP.Method)
	path := req.RequestContext.HTTP.Path
	switch {
	case method == "GET":
		return handleList(ctx, logger, req)
	case method == "POST" && strings.HasSuffix(path, "/apply"):
		return handleDecision(ctx, logger, req, true)
	case method == "POST" && strings.HasSuffix(path, "/dismiss"):
		return handleDecision(ctx, logger, req, false)
	default:
		return httpresp.Error(405, "method not allowed"), nil
	}
}

func handleList(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	status := req.QueryStringParameters["status"]
	if status == "" {
		status = "pending"
	}
	if !allowedStatuses[status] {
		return httpresp.Error(400, fmt.Sprintf("unsupported status %q", status)), nil
	}
	limit := int32(defaultLimit)
	client, err := ddb.Client(ctx)
	if err != nil {
		logger.Error("api-tuners.ddb", "err", err)
		return httpresp.Error(500, "could not load tuners"), nil
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		IndexName:              aws.String(gsi1Index),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk": &dtypes.AttributeValueMemberS{Value: "DELTA#STATUS#" + status},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(limit),
	})
	if err != nil {
		logger.Error("api-tuners.list", "err", err, "status", status)
		return httpresp.Error(500, "could not load tuners"), nil
	}
	items := make([]tunerItem, 0, len(out.Items))
	for _, raw := range out.Items {
		var d tunerlib.TunerDelta
		if err := attributevalue.UnmarshalMap(raw, &d); err != nil {
			return httpresp.Error(500, "could not load tuners"), nil
		}
		items = append(items, tunerItem{
			Ref:       encodeRef(d.PK, d.SK),
			ID:        d.ID,
			Kind:      d.Kind,
			Vertical:  d.Vertical,
			Status:    d.Status,
			Rationale: d.Rationale,
			Proposed:  json.RawMessage(d.ProposedPayload),
			PromptID:  d.PromptID,
			CreatedAt: d.CreatedAt,
		})
	}
	body, _ := json.Marshal(tunersResponse{Status: status, Items: items})
	return httpresp.JSON(200, string(body)), nil
}

type decisionBody struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

func handleDecision(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest, apply bool) (events.APIGatewayV2HTTPResponse, error) {
	var body decisionBody
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		return httpresp.Error(400, "invalid JSON body"), nil
	}
	pk, sk, err := decodeRef(body.Ref)
	if err != nil {
		return httpresp.Error(400, "invalid ref"), nil
	}
	d, err := getDelta(ctx, pk, sk)
	if err != nil {
		return httpresp.Error(404, "tuner delta not found"), nil
	}
	if d.Status != "pending" {
		return httpresp.Error(409, fmt.Sprintf("delta already %s", d.Status)), nil
	}

	actor := auth.Sub(req)
	now := time.Now().UTC().Format(time.RFC3339)

	newVersion := 0
	if apply {
		newVersion, err = applyDelta(ctx, d)
		if err != nil {
			logger.Error("api-tuners.apply", "err", err, "kind", d.Kind, "vertical", d.Vertical)
			return httpresp.Error(500, "could not apply delta"), nil
		}
	}

	newStatus := "dismissed"
	action := feedback.ActionDismiss
	if apply {
		newStatus = "applied"
		action = feedback.ActionApply
	}
	if err := setDeltaStatus(ctx, pk, sk, newStatus, actor, now); err != nil {
		if errors.Is(err, errNoLongerPending) {
			return httpresp.Error(409, "delta already decided by another request"), nil
		}
		logger.Error("api-tuners.status", "err", err)
		return httpresp.Error(500, "decision recorded partially — retry"), nil
	}

	publisher, perr := newPublisher(ctx)
	if perr != nil {
		logger.Error("api-tuners.publisher", "err", perr)
		return httpresp.Error(500, "could not record decision"), nil
	}
	notes := body.Reason
	if apply {
		notes = fmt.Sprintf("applied %s tuner delta (v%d)", d.Kind, newVersion)
	}
	if _, _, err := feedback.Capture(ctx, feedback.CaptureInput{
		Subject:   feedback.SubjectProfile,
		SubjectID: d.ID,
		Actor:     actor,
		Action:    action,
		Vertical:  d.Vertical,
		Notes:     notes,
	}, publisher); err != nil {
		logger.Error("api-tuners.feedback", "err", err)
		return httpresp.Error(500, "could not write audit row"), nil
	}

	if apply {
		if err := pkgevents.Publish(ctx, publisher, pkgevents.New("profile.updated", "api-tuners", profileUpdatedDetail{
			Kind: d.Kind, Vertical: d.Vertical, Version: newVersion, DeltaID: d.ID,
		})); err != nil {
			logger.Error("api-tuners.publish", "err", err)
			return httpresp.Error(500, "applied but event not emitted — retry"), nil
		}
	}
	return httpresp.JSON(200, `{"status":"ok"}`), nil
}

type profileUpdatedDetail struct {
	Kind     string `json:"kind"`
	Vertical string `json:"vertical"`
	Version  int    `json:"version,omitempty"`
	DeltaID  string `json:"deltaId"`
}

// applyDelta mutates the live profile from the proposal and returns
// the new version (0 for advisory kinds that have no canonical
// per-vertical profile, i.e. targeting at MVP).
func applyDelta(ctx context.Context, d tunerlib.TunerDelta) (int, error) {
	switch d.Kind {
	case "style":
		var p schemas.TunerStyleV1
		if err := json.Unmarshal([]byte(d.ProposedPayload), &p); err != nil {
			return 0, fmt.Errorf("parse style delta: %w", err)
		}
		g, err := style.Get(ctx, d.Vertical)
		if err != nil {
			return 0, fmt.Errorf("load style guide %s: %w", d.Vertical, err)
		}
		g.DoPhrases = mergeList(g.DoPhrases, p.AddDoPhrases, p.RemoveDoPhrases)
		g.DontPhrases = mergeList(g.DontPhrases, p.AddDontPhrases, p.RemoveDontPhrases)
		g.AntiPatterns = mergeList(g.AntiPatterns, p.AddAntiPatterns, nil)
		if p.PaletteSuggestions != nil && p.PaletteSuggestions.Primary != nil && *p.PaletteSuggestions.Primary != "" {
			g.Palette.Primary = *p.PaletteSuggestions.Primary
		}
		updated, err := style.Update(ctx, d.Vertical, g, g.Etag)
		if err != nil {
			return 0, fmt.Errorf("update style guide: %w", err)
		}
		return updated.Version, nil

	case "email_tone":
		var p schemas.TunerEmailToneV1
		if err := json.Unmarshal([]byte(d.ProposedPayload), &p); err != nil {
			return 0, fmt.Errorf("parse email-tone delta: %w", err)
		}
		prof, err := tone.Get(ctx, d.Vertical)
		if err != nil {
			return 0, fmt.Errorf("load tone profile %s: %w", d.Vertical, err)
		}
		prof.SubjectPatterns = mergeList(prof.SubjectPatterns, p.AddSubjectPatterns, p.RemoveSubjectPatterns)
		prof.OpenerPatterns = mergeList(prof.OpenerPatterns, p.AddOpenerPatterns, p.RemoveOpenerPatterns)
		prof.ProhibitedPhrases = mergeList(prof.ProhibitedPhrases, p.AddProhibitedPhrases, p.RemoveProhibitedPhrases)
		updated, err := tone.Update(ctx, d.Vertical, prof, prof.Etag)
		if err != nil {
			return 0, fmt.Errorf("update tone profile: %w", err)
		}
		return updated.Version, nil

	case "targeting":
		// Advisory at MVP — no canonical per-vertical TargetingProfile
		// to mutate. The delta is still recorded + audited + evented;
		// the operator applies keyword/weight nudges via
		// /settings/targeting.
		return 0, nil
	default:
		return 0, fmt.Errorf("unknown delta kind %q", d.Kind)
	}
}

// mergeList appends `add` (deduped, order-preserving) then removes
// every entry in `remove`. Trims blanks.
func mergeList(cur, add, remove []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(cur)+len(add))
	keep := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range cur {
		keep(s)
	}
	for _, s := range add {
		keep(s)
	}
	if len(remove) == 0 {
		return out
	}
	drop := map[string]bool{}
	for _, s := range remove {
		drop[strings.TrimSpace(s)] = true
	}
	final := out[:0]
	for _, s := range out {
		if !drop[s] {
			final = append(final, s)
		}
	}
	return final
}

func getDelta(ctx context.Context, pk, sk string) (tunerlib.TunerDelta, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return tunerlib.TunerDelta{}, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: pk},
			"sk": &dtypes.AttributeValueMemberS{Value: sk},
		},
	})
	if err != nil {
		return tunerlib.TunerDelta{}, err
	}
	if len(out.Item) == 0 {
		return tunerlib.TunerDelta{}, errors.New("not found")
	}
	var d tunerlib.TunerDelta
	if err := attributevalue.UnmarshalMap(out.Item, &d); err != nil {
		return tunerlib.TunerDelta{}, err
	}
	return d, nil
}

func setDeltaStatus(ctx context.Context, pk, sk, status, actor, now string) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	_, err = client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: pk},
			"sk": &dtypes.AttributeValueMemberS{Value: sk},
		},
		UpdateExpression: aws.String("SET #s = :s, gsi1pk = :g, decidedBy = :by, decidedAt = :ts, updatedAt = :ts"),
		ExpressionAttributeNames: map[string]string{
			"#s": "status",
		},
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":s":  &dtypes.AttributeValueMemberS{Value: status},
			":g":  &dtypes.AttributeValueMemberS{Value: "DELTA#STATUS#" + status},
			":by": &dtypes.AttributeValueMemberS{Value: actor},
			":ts": &dtypes.AttributeValueMemberS{Value: now},
			":p":  &dtypes.AttributeValueMemberS{Value: "pending"},
		},
		ConditionExpression: aws.String("attribute_exists(pk) AND #s = :p"),
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return errNoLongerPending
		}
		return fmt.Errorf("update delta status: %w", err)
	}
	return nil
}

func encodeRef(pk, sk string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(pk + "|" + sk))
}

func decodeRef(ref string) (pk, sk string, err error) {
	b, derr := base64.RawURLEncoding.DecodeString(ref)
	if derr != nil {
		return "", "", derr
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("malformed ref")
	}
	return parts[0], parts[1], nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
