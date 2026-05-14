// Package main is the api-specs Lambda. Operator-only BFF routes for
// the /queue/[id] page from .ralph/specs/08-admin-ui.md:
//
//	GET    /candidates/{businessId}                       — load business + latest spec + audit summary
//	GET    /candidates/{businessId}/specs                 — list specs for a business
//	PATCH  /candidates/{businessId}/specs/{specId}        — edit spec.content (status stays draft, version bumps)
//	POST   /candidates/{businessId}/specs/{specId}/approve — set status=approved; publish spec.approved + feedback.captured
//	POST   /candidates/{businessId}/specs/{specId}/reject  — set status=rejected; publish spec.rejected + feedback.captured
//
// Every Approve / Edit / Reject also emits `feedback.captured` (iter
// 9.x tuner Lambdas consume these for style/tone retraining).
package main

import (
	"context"
	"encoding/json"
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
)

// SpecRow mirrors lambdas/spec-generator's row shape (duplicated; sibling package main).
type SpecRow struct {
	PK         string         `dynamodbav:"pk"`
	SK         string         `dynamodbav:"sk"`
	Type       string         `dynamodbav:"type"`
	ID         string         `dynamodbav:"id"`
	Version    int            `dynamodbav:"version"`
	Status     string         `dynamodbav:"status"`
	Content    schemas.SpecV1 `dynamodbav:"content"`
	ModelID    string         `dynamodbav:"modelId"`
	PromptID   string         `dynamodbav:"promptId"`
	ApprovedBy string         `dynamodbav:"approvedBy,omitempty"`
	ApprovedAt string         `dynamodbav:"approvedAt,omitempty"`
	CreatedAt  string         `dynamodbav:"createdAt"`
	UpdatedAt  string         `dynamodbav:"updatedAt"`
	Etag       string         `dynamodbav:"etag"`
}

// BusinessRow is the subset of Business we return alongside the spec
// so the candidate page can render the header without a second call.
type BusinessRow struct {
	ID       string `dynamodbav:"id"       json:"id"`
	Name     string `dynamodbav:"name"     json:"name"`
	Domain   string `dynamodbav:"domain"   json:"domain"`
	Vertical string `dynamodbav:"vertical" json:"vertical"`
	Location string `dynamodbav:"location" json:"location"`
	Status   string `dynamodbav:"status"   json:"status"`
}

// candidateResponse is the shape the /queue/[id] page consumes — one
// trip returns everything that visible-without-scrolling needs.
type candidateResponse struct {
	Business BusinessRow `json:"business"`
	Spec     *SpecRow    `json:"spec,omitempty"` // nil when no spec exists yet
}

func handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	logger := applog.FromContext(ctx)

	if !auth.IsOperator(req) {
		logger.Info("forbidden", "route", req.RouteKey)
		return httpresp.Error(403, "operator group required"), nil
	}

	method := strings.ToUpper(req.RequestContext.HTTP.Method)
	path := req.RequestContext.HTTP.Path
	businessID := req.PathParameters["businessId"]
	specID := req.PathParameters["specId"]

	switch {
	case method == "GET" && specID == "" && strings.HasSuffix(path, "/specs"):
		return handleListSpecs(ctx, logger, businessID)
	case method == "GET" && specID == "":
		return handleGetCandidate(ctx, logger, businessID)
	case method == "PATCH" && specID != "":
		return handlePatchSpec(ctx, logger, req, businessID, specID)
	case method == "POST" && strings.HasSuffix(path, "/approve"):
		return handleApprove(ctx, logger, req, businessID, specID)
	case method == "POST" && strings.HasSuffix(path, "/reject"):
		return handleReject(ctx, logger, req, businessID, specID)
	default:
		return httpresp.Error(405, "method not allowed"), nil
	}
}

// --- GET /candidates/{businessId} ---------------------------------------

func handleGetCandidate(ctx context.Context, logger *slog.Logger, businessID string) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" {
		return httpresp.Error(400, "businessId is required"), nil
	}
	biz, err := getBusiness(ctx, businessID)
	if err != nil {
		logger.Error("api-specs.getBusiness failed", "err", err)
		return httpresp.Error(500, "could not load business"), nil
	}
	if biz == nil {
		return httpresp.Error(404, "business not found"), nil
	}
	specs, err := listSpecs(ctx, businessID)
	if err != nil {
		logger.Error("api-specs.listSpecs failed", "err", err)
		return httpresp.Error(500, "could not load specs"), nil
	}
	var latest *SpecRow
	if len(specs) > 0 {
		latest = &specs[0] // listSpecs returns newest first
	}
	body, _ := json.Marshal(candidateResponse{Business: *biz, Spec: latest})
	return httpresp.JSON(200, string(body)), nil
}

// --- GET /candidates/{businessId}/specs --------------------------------

func handleListSpecs(ctx context.Context, logger *slog.Logger, businessID string) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" {
		return httpresp.Error(400, "businessId is required"), nil
	}
	specs, err := listSpecs(ctx, businessID)
	if err != nil {
		logger.Error("api-specs.listSpecs failed", "err", err)
		return httpresp.Error(500, "could not load specs"), nil
	}
	body, _ := json.Marshal(specs)
	return httpresp.JSON(200, string(body)), nil
}

// --- PATCH /candidates/{businessId}/specs/{specId} ---------------------
//
// Request body: `{ "content": <SpecV1>, "notes": "…" }`. notes is
// captured on the feedback row; content replaces the row's content and
// bumps version + etag.

type patchSpecBody struct {
	Content schemas.SpecV1 `json:"content"`
	Notes   string         `json:"notes,omitempty"`
}

func handlePatchSpec(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest, businessID, specID string) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" || specID == "" {
		return httpresp.Error(400, "businessId and specId are required"), nil
	}
	var body patchSpecBody
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		return httpresp.Error(400, fmt.Sprintf("invalid JSON body: %v", err)), nil
	}
	if err := schemas.ValidateSpecV1Structural(body.Content); err != nil {
		return httpresp.Error(400, fmt.Sprintf("spec content failed validation: %v", err)), nil
	}

	current, err := getSpec(ctx, businessID, specID)
	if err != nil {
		logger.Error("api-specs.getSpec failed", "err", err)
		return httpresp.Error(500, "could not load spec"), nil
	}
	if current == nil {
		return httpresp.Error(404, "spec not found"), nil
	}

	originalJSON, _ := json.Marshal(current.Content)
	editedJSON, _ := json.Marshal(body.Content)

	updated := *current
	updated.Content = body.Content
	updated.Version = current.Version + 1
	updated.UpdatedAt = nowFunc().UTC().Format(time.RFC3339)
	updated.Etag = randomHexFn(16)
	if err := putSpec(ctx, updated); err != nil {
		logger.Error("api-specs.putSpec failed", "err", err)
		return httpresp.Error(500, "could not save spec"), nil
	}

	if err := captureFeedback(ctx, feedback.CaptureInput{
		Subject:         feedback.SubjectSpec,
		SubjectID:       specID,
		BusinessID:      businessID,
		Actor:           auth.Sub(req),
		Action:          feedback.ActionEdit,
		OriginalPayload: string(originalJSON),
		EditedPayload:   string(editedJSON),
		Notes:           body.Notes,
		Vertical:        verticalFor(ctx, businessID),
	}); err != nil {
		logger.Error("api-specs.feedback.capture failed", "err", err)
	}

	out, _ := json.Marshal(updated)
	return httpresp.JSON(200, string(out)), nil
}

// --- POST /candidates/{businessId}/specs/{specId}/approve --------------

type approveRejectBody struct {
	Notes string `json:"notes,omitempty"`
}

func handleApprove(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest, businessID, specID string) (events.APIGatewayV2HTTPResponse, error) {
	return handleDecision(ctx, logger, req, businessID, specID, "approved", "spec.approved", feedback.ActionApprove)
}

// --- POST /candidates/{businessId}/specs/{specId}/reject ---------------

func handleReject(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest, businessID, specID string) (events.APIGatewayV2HTTPResponse, error) {
	return handleDecision(ctx, logger, req, businessID, specID, "rejected", "spec.rejected", feedback.ActionReject)
}

// handleDecision is the shared approve/reject path — flips status,
// stamps approvedBy/approvedAt, publishes the spec.<approved|rejected>
// event, captures feedback.
func handleDecision(
	ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest,
	businessID, specID, newStatus, eventName, feedbackAction string,
) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" || specID == "" {
		return httpresp.Error(400, "businessId and specId are required"), nil
	}
	var body approveRejectBody
	if req.Body != "" {
		if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
			return httpresp.Error(400, fmt.Sprintf("invalid JSON body: %v", err)), nil
		}
	}

	current, err := getSpec(ctx, businessID, specID)
	if err != nil {
		logger.Error("api-specs.getSpec failed", "err", err)
		return httpresp.Error(500, "could not load spec"), nil
	}
	if current == nil {
		return httpresp.Error(404, "spec not found"), nil
	}
	if current.Status != "draft" {
		return httpresp.Error(409, fmt.Sprintf("spec is %q; can only decide on a draft", current.Status)), nil
	}

	actor := auth.Sub(req)
	now := nowFunc().UTC().Format(time.RFC3339)

	updated := *current
	updated.Status = newStatus
	updated.ApprovedBy = actor
	updated.ApprovedAt = now
	updated.UpdatedAt = now
	updated.Etag = randomHexFn(16)
	if err := putSpec(ctx, updated); err != nil {
		logger.Error("api-specs.putSpec failed", "err", err)
		return httpresp.Error(500, "could not save spec"), nil
	}

	// Publish spec.<approved|rejected> first; feedback.captured is
	// the audit trail and is more forgiving of a transient failure
	// (recoverable from the gsi2 scan).
	if err := publishSpecDecision(ctx, eventName, businessID, specID, current.Version); err != nil {
		logger.Error("api-specs.publish failed", "err", err)
		return httpresp.Error(502, "could not publish event"), nil
	}

	originalJSON, _ := json.Marshal(current.Content)
	if err := captureFeedback(ctx, feedback.CaptureInput{
		Subject:         feedback.SubjectSpec,
		SubjectID:       specID,
		BusinessID:      businessID,
		Actor:           actor,
		Action:          feedbackAction,
		OriginalPayload: string(originalJSON),
		Notes:           body.Notes,
		Vertical:        verticalFor(ctx, businessID),
	}); err != nil {
		logger.Error("api-specs.feedback.capture failed", "err", err)
	}

	out, _ := json.Marshal(updated)
	return httpresp.JSON(200, string(out)), nil
}

// --- DDB helpers -------------------------------------------------------

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
		return nil, fmt.Errorf("get business: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var b BusinessRow
	if err := attributevalue.UnmarshalMap(out.Item, &b); err != nil {
		return nil, fmt.Errorf("unmarshal business: %w", err)
	}
	return &b, nil
}

func getSpec(ctx context.Context, businessID, specID string) (*SpecRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "SPEC#" + specID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get spec: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var s SpecRow
	if err := attributevalue.UnmarshalMap(out.Item, &s); err != nil {
		return nil, fmt.Errorf("unmarshal spec: %w", err)
	}
	return &s, nil
}

// listSpecs returns all specs for a business, newest first.
func listSpecs(ctx context.Context, businessID string) ([]SpecRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk":     &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			":prefix": &dtypes.AttributeValueMemberS{Value: "SPEC#"},
		},
		ScanIndexForward: aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("query specs: %w", err)
	}
	specs := make([]SpecRow, 0, len(out.Items))
	for _, item := range out.Items {
		var s SpecRow
		if err := attributevalue.UnmarshalMap(item, &s); err != nil {
			return nil, fmt.Errorf("unmarshal spec row: %w", err)
		}
		specs = append(specs, s)
	}
	return specs, nil
}

func putSpec(ctx context.Context, row SpecRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put spec: %w", err)
	}
	return nil
}

// verticalFor returns the business.vertical so the Feedback row's gsi2
// partition is keyed correctly. Best-effort — falls back to empty
// string (feedback.Capture promotes that to "default") on lookup
// failure.
func verticalFor(ctx context.Context, businessID string) string {
	biz, err := getBusiness(ctx, businessID)
	if err != nil || biz == nil {
		return ""
	}
	return biz.Vertical
}

// publishSpecDecision emits `spec.approved` or `spec.rejected`.
// Shape matches 03-events.md: `{businessId, specId, version}`.
func publishSpecDecision(ctx context.Context, eventName, businessID, specID string, version int) error {
	publisher, err := publisherProvider(ctx)
	if err != nil {
		return fmt.Errorf("api-specs: publisher: %w", err)
	}
	env := pkgevents.New(eventName, "api-specs", map[string]any{
		"businessId": businessID,
		"specId":     specID,
		"version":    version,
	})
	return pkgevents.Publish(ctx, publisher, env)
}

// captureFeedback wraps pkg/feedback.Capture with the api-specs'
// shared publisher.
func captureFeedback(ctx context.Context, in feedback.CaptureInput) error {
	publisher, err := publisherProvider(ctx)
	if err != nil {
		return fmt.Errorf("api-specs: publisher: %w", err)
	}
	_, _, err = feedback.Capture(ctx, in, publisher)
	return err
}

// --- AWS wiring (lazy) -------------------------------------------------

var (
	cachedPublisher *pkgevents.Publisher
	nowFunc         = func() time.Time { return time.Now().UTC() }
	randomHexFn     = defaultRandomHex
)

func publisherProvider(ctx context.Context) (*pkgevents.Publisher, error) {
	if cachedPublisher != nil {
		return cachedPublisher, nil
	}
	p, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return nil, err
	}
	cachedPublisher = p
	return p, nil
}

func defaultRandomHex(n int) string {
	// Reuse the events package's UUID format would force a dep on
	// google/uuid here; the existing project pattern is hex bytes.
	// Use a deterministic prefix the spec generator already uses.
	return fmt.Sprintf("%016x", time.Now().UnixNano())
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
