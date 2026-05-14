// Package main is the qualifier Lambda. EventBridge routes
// `website.audit.completed` events here via an SQS main queue (with
// DLQ); the rule pattern restricts to `detail.worthRedesigning = true`
// so the qualifier doesn't bother scoring sites the audit already
// declared not worth a redesign.
//
// Per-record pipeline:
//
//  1. Decode the envelope (events.FromSQS).
//  2. idempotency.WithIdempotency("qualifier", env.EventID, …).
//  3. Load Audit + Business rows; look up the TargetingProfile by
//     vertical (v1 — profileId propagation lands later).
//  4. qualifier.PriorityScore — pure formula from
//     05-capacity-and-cost.md. Result is in [0, 1].
//  5. Compare priorityScore*100 to
//     PipelineSettings.Thresholds.MinQualificationScore (default 70).
//  6. Persist a Qualification row with `qualified: true|false`,
//     priorityScore, reasons, auditId, targetingProfileId.
//  7. Update Business.status to "qualified" or "rejected".
//  8. Publish website.qualified OR website.rejected, with correlation
//     + causation propagated from the inbound envelope.
//
// Entry-level killswitch wraps StageAudit (the qualifier rolls up to
// the audit stage per killswitch.StageMap). SQS retry budget = 3 on
// the queue; partial-batch-failure surfaces per-record errors so SQS
// retries just the failing record.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/qualifier"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/targeting"
)

const consumerName = "qualifier"

// AuditCompletedDetail mirrors lambdas/audit/main.go's payload.
// Duplicated here rather than imported because that lives in package main.
type AuditCompletedDetail struct {
	BusinessID       string   `json:"businessId"`
	AuditID          string   `json:"auditId"`
	Score            int      `json:"score"`
	WorthRedesigning bool     `json:"worthRedesigning"`
	Priority         string   `json:"priority,omitempty"`
	ModelsUsed       []string `json:"modelsUsed"`
}

// WebsiteQualifiedDetail is the published payload for `website.qualified`
// per .ralph/specs/03-events.md.
type WebsiteQualifiedDetail struct {
	BusinessID      string  `json:"businessId"`
	QualificationID string  `json:"qualificationId"`
	PriorityScore   float64 `json:"priorityScore"`
	AuditID         string  `json:"auditId"`
}

// WebsiteRejectedDetail is the published payload for `website.rejected`.
// The spec lists the event name but not the schema; we mirror the
// qualified shape plus a `reasons` array so the operator UI can show
// "why" without re-reading the Qualification row.
type WebsiteRejectedDetail struct {
	BusinessID      string   `json:"businessId"`
	QualificationID string   `json:"qualificationId"`
	PriorityScore   float64  `json:"priorityScore"`
	AuditID         string   `json:"auditId"`
	Reasons         []string `json:"reasons,omitempty"`
}

// runDeps is the testable surface. The production `handle` wires real
// implementations; tests inject fakes.
type runDeps struct {
	GetAudit         func(ctx context.Context, businessID, auditID string) (*AuditRow, error)
	GetBusiness      func(ctx context.Context, businessID string) (*BusinessRow, error)
	ListProfiles     func(ctx context.Context) ([]targeting.Profile, error)
	PutQualification func(ctx context.Context, row QualificationRow) error
	UpdateBusiness   func(ctx context.Context, businessID, newStatus string) error
	PublishQualified func(ctx context.Context, env pkgevents.Envelope[WebsiteQualifiedDetail]) error
	PublishRejected  func(ctx context.Context, env pkgevents.Envelope[WebsiteRejectedDetail]) error
	Threshold        func(ctx context.Context) (int, error)
	Now              func() time.Time
	NewQualID        func() string
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	err := killswitch.WithKillSwitch(ctx, killswitch.StageAudit, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		out, err := pkgevents.Consume[AuditCompletedDetail](ctx, raw, func(ctx context.Context, env pkgevents.Envelope[AuditCompletedDetail]) error {
			return processRecord(ctx, deps, env)
		})
		resp = out
		return err
	})
	return resp, err
}

// processRecord is the per-record handler wrapped in idempotency.
func processRecord(ctx context.Context, d runDeps, env pkgevents.Envelope[AuditCompletedDetail]) error {
	logger := applog.FromContext(ctx).With(
		"eventId", env.EventID,
		"businessId", env.Detail.BusinessID,
		"auditId", env.Detail.AuditID,
	)

	_, err := idempotency.WithIdempotency(ctx, consumerName, env.EventID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, runOne(ctx, d, env, logger)
	})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("qualifier.replay.skipped")
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, env pkgevents.Envelope[AuditCompletedDetail], logger *slog.Logger) error {
	audit, err := d.GetAudit(ctx, env.Detail.BusinessID, env.Detail.AuditID)
	if err != nil {
		return fmt.Errorf("qualifier: get audit: %w", err)
	}
	if audit == nil {
		logger.Warn("qualifier.audit.missing")
		return nil
	}

	biz, err := d.GetBusiness(ctx, env.Detail.BusinessID)
	if err != nil {
		return fmt.Errorf("qualifier: get business: %w", err)
	}
	if biz == nil {
		logger.Warn("qualifier.business.missing")
		return nil
	}

	profile, err := findProfileForBusiness(ctx, d, biz.Vertical)
	if err != nil {
		return fmt.Errorf("qualifier: find profile: %w", err)
	}
	if profile == nil {
		logger.Warn("qualifier.profile.missing", "vertical", biz.Vertical)
		return nil
	}

	threshold, err := d.Threshold(ctx)
	if err != nil {
		return fmt.Errorf("qualifier: lookup threshold: %w", err)
	}

	in := buildScoreInput(audit, biz, *profile)
	priorityScore := qualifier.PriorityScore(in)
	qualified := priorityScore*100 >= float64(threshold)

	reasons := buildReasons(audit, biz, *profile, qualified)
	qualID := d.NewQualID()
	now := d.Now().UTC().Format(time.RFC3339)

	row := QualificationRow{
		PK:                 "BUSINESS#" + biz.ID,
		SK:                 "QUAL#" + qualID,
		Type:               "Qualification",
		ID:                 qualID,
		Qualified:          qualified,
		PriorityScore:      priorityScore,
		Reasons:            reasons,
		AuditID:            audit.ID,
		TargetingProfileID: profile.ID,
		CreatedAt:          now,
	}
	if err := d.PutQualification(ctx, row); err != nil {
		return fmt.Errorf("qualifier: put qualification: %w", err)
	}

	newStatus := "rejected"
	if qualified {
		newStatus = "qualified"
	}
	if err := d.UpdateBusiness(ctx, biz.ID, newStatus); err != nil {
		// Status update failure isn't fatal — the Qualification row is
		// the source of truth and downstream consumers read it. Log
		// + carry on.
		logger.Error("qualifier.business.update_status_failed", "err", err)
	}

	if qualified {
		out := pkgevents.New("website.qualified", consumerName, WebsiteQualifiedDetail{
			BusinessID:      biz.ID,
			QualificationID: qualID,
			PriorityScore:   priorityScore,
			AuditID:         audit.ID,
		}).WithCorrelation(env.CorrelationID).WithCausation(env.EventID)
		if err := d.PublishQualified(ctx, out); err != nil {
			return fmt.Errorf("qualifier: publish website.qualified: %w", err)
		}
	} else {
		out := pkgevents.New("website.rejected", consumerName, WebsiteRejectedDetail{
			BusinessID:      biz.ID,
			QualificationID: qualID,
			PriorityScore:   priorityScore,
			AuditID:         audit.ID,
			Reasons:         reasons,
		}).WithCorrelation(env.CorrelationID).WithCausation(env.EventID)
		if err := d.PublishRejected(ctx, out); err != nil {
			return fmt.Errorf("qualifier: publish website.rejected: %w", err)
		}
	}
	logger.Info("qualifier.decided",
		"qualified", qualified,
		"priorityScore", priorityScore,
		"threshold", threshold,
		"qualificationId", qualID,
	)
	return nil
}

// findProfileForBusiness picks the targeting profile to use for
// scoring. v1 strategy: scan all enabled profiles, return the
// most-recently-updated one whose vertical matches the business's.
// If no match, return nil so the caller logs + skips. Iter
// 6.x will revisit by propagating profileId through the event chain.
func findProfileForBusiness(ctx context.Context, d runDeps, vertical string) (*targeting.Profile, error) {
	profiles, err := d.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	var matches []targeting.Profile
	for _, p := range profiles {
		if p.Enabled && eqFoldVertical(p.Vertical, vertical) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].UpdatedAt > matches[j].UpdatedAt
	})
	return &matches[0], nil
}

// buildScoreInput projects the Audit + Business rows onto the minimal
// `qualifier.Input` shape. Contact is nil for v1 — iter 6.x adds
// contact enrichment.
func buildScoreInput(audit *AuditRow, biz *BusinessRow, profile targeting.Profile) qualifier.Input {
	return qualifier.Input{
		Audit: qualifier.Audit{
			Score:                 audit.Score,
			LighthousePerformance: audit.Technical.Lighthouse.Performance,
			HasContact:            audit.Technical.ContactDetected,
		},
		Business: qualifier.Business{
			Vertical:   biz.Vertical,
			Confidence: biz.Confidence,
		},
		Contact:          nil,
		TargetingProfile: profile,
	}
}

// buildReasons produces a small list of human-readable reasons the
// /candidates UI surfaces alongside the decision. Deterministic
// functions of the inputs — same audit always yields same reasons.
func buildReasons(audit *AuditRow, biz *BusinessRow, profile targeting.Profile, qualified bool) []string {
	var reasons []string

	if audit.Qualitative != nil {
		highSev := 0
		for _, i := range audit.Qualitative.Issues {
			if i.Severity == "high" {
				highSev++
			}
		}
		if highSev > 0 {
			reasons = append(reasons, fmt.Sprintf("%d high-severity audit issues", highSev))
		}
	}
	if audit.Technical.Lighthouse.Performance > 0 && audit.Technical.Lighthouse.Performance < 30 {
		reasons = append(reasons, "very slow site")
	}
	if !audit.Technical.ContactDetected {
		reasons = append(reasons, "contact details hard to find")
	}
	if !audit.Technical.HTTPS {
		reasons = append(reasons, "site is not HTTPS")
	}
	if eqFoldVertical(biz.Vertical, profile.Vertical) {
		reasons = append(reasons, "vertical fit high")
	}
	if biz.Confidence >= 0.7 {
		reasons = append(reasons, "high-confidence discovery source")
	}
	if !qualified && audit.Score >= 80 {
		reasons = append(reasons, "site is already well-converted")
	}
	if len(reasons) == 0 {
		reasons = []string{"score below threshold"}
	}
	return reasons
}

// eqFoldVertical is a case-insensitive ASCII compare for vertical
// strings. Duplicated from qualifier.eqFold rather than exported.
func eqFoldVertical(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// --- row shapes ----------------------------------------------------------

// AuditRow mirrors the subset of lambdas/audit's AuditRow this Lambda
// reads. Field tags match the source so DDB unmarshal round-trips.
type AuditRow struct {
	ID               string            `dynamodbav:"id"`
	Score            int               `dynamodbav:"score"`
	WorthRedesigning bool              `dynamodbav:"worthRedesigning"`
	Technical        AuditTechnical    `dynamodbav:"technical"`
	Qualitative      *AuditQualitative `dynamodbav:"qualitative,omitempty"`
}

type AuditTechnical struct {
	HTTPS           bool            `dynamodbav:"https"`
	Viewport        bool            `dynamodbav:"viewport"`
	Favicon         bool            `dynamodbav:"favicon"`
	ContactDetected bool            `dynamodbav:"contactDetected"`
	Lighthouse      AuditLighthouse `dynamodbav:"lighthouse"`
}

type AuditLighthouse struct {
	Performance   int `dynamodbav:"performance"`
	Accessibility int `dynamodbav:"accessibility"`
	SEO           int `dynamodbav:"seo"`
}

type AuditQualitative struct {
	ModelID string           `dynamodbav:"modelId"`
	Summary string           `dynamodbav:"summary"`
	Issues  []AuditQualIssue `dynamodbav:"issues"`
}

type AuditQualIssue struct {
	Type        string `dynamodbav:"type"`
	Severity    string `dynamodbav:"severity"`
	Description string `dynamodbav:"description"`
}

// BusinessRow is the subset of the Business item we read.
type BusinessRow struct {
	ID         string  `dynamodbav:"id"`
	Name       string  `dynamodbav:"name"`
	Domain     string  `dynamodbav:"domain"`
	Vertical   string  `dynamodbav:"vertical"`
	Location   string  `dynamodbav:"location"`
	Source     string  `dynamodbav:"source"`
	Confidence float64 `dynamodbav:"confidence"`
	Status     string  `dynamodbav:"status"`
}

// QualificationRow mirrors 02-data-model.md § "Qualification".
type QualificationRow struct {
	PK                 string   `dynamodbav:"pk"`
	SK                 string   `dynamodbav:"sk"`
	Type               string   `dynamodbav:"type"`
	ID                 string   `dynamodbav:"id"`
	Qualified          bool     `dynamodbav:"qualified"`
	PriorityScore      float64  `dynamodbav:"priorityScore"`
	Reasons            []string `dynamodbav:"reasons"`
	AuditID            string   `dynamodbav:"auditId"`
	TargetingProfileID string   `dynamodbav:"targetingProfileId"`
	CreatedAt          string   `dynamodbav:"createdAt"`
}

// --- AWS wiring (production) ---------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	return runDeps{
		GetAudit:         getAudit,
		GetBusiness:      getBusiness,
		ListProfiles:     targeting.List,
		PutQualification: putQualification,
		UpdateBusiness:   updateBusinessStatus,
		PublishQualified: func(ctx context.Context, env pkgevents.Envelope[WebsiteQualifiedDetail]) error {
			return pkgevents.Publish(ctx, publisher, env)
		},
		PublishRejected: func(ctx context.Context, env pkgevents.Envelope[WebsiteRejectedDetail]) error {
			return pkgevents.Publish(ctx, publisher, env)
		},
		Threshold: thresholdFromSettings,
		Now:       time.Now,
		NewQualID: func() string { return randomHex(16) },
	}, nil
}

func thresholdFromSettings(ctx context.Context) (int, error) {
	s, err := killswitch.Get(ctx)
	if err != nil {
		return 0, err
	}
	return s.Thresholds.MinQualificationScore, nil
}

func getAudit(ctx context.Context, businessID, auditID string) (*AuditRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "AUDIT#" + auditID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get audit %s/%s: %w", businessID, auditID, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var row AuditRow
	if err := attributevalue.UnmarshalMap(out.Item, &row); err != nil {
		return nil, fmt.Errorf("unmarshal audit %s: %w", auditID, err)
	}
	return &row, nil
}

func getBusiness(ctx context.Context, businessID string) (*BusinessRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get business %s: %w", businessID, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var row BusinessRow
	if err := attributevalue.UnmarshalMap(out.Item, &row); err != nil {
		return nil, fmt.Errorf("unmarshal business %s: %w", businessID, err)
	}
	return &row, nil
}

func putQualification(ctx context.Context, row QualificationRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal qualification: %w", err)
	}
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("put qualification %s: %w", row.ID, err)
	}
	return nil
}

// updateBusinessStatus flips Business.status and refreshes gsi1pk +
// updatedAt. gsi1 is keyed on status, so the metrics widget needs the
// row to move partitions when the status changes.
func updateBusinessStatus(ctx context.Context, businessID, newStatus string) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		},
		UpdateExpression: aws.String("SET #s = :s, gsi1pk = :pk, updatedAt = :ts"),
		ExpressionAttributeNames: map[string]string{
			"#s": "status",
		},
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":s":  &dtypes.AttributeValueMemberS{Value: newStatus},
			":pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#" + newStatus},
			":ts": &dtypes.AttributeValueMemberS{Value: now},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			// Business row vanished between read + write — treat as
			// already-handled rather than DLQ.
			return nil
		}
		return fmt.Errorf("update business %s status: %w", businessID, err)
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("qualifier: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
