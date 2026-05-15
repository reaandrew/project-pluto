// Package main is the api-website Lambda. Operator-only BFF routes for
// the site-preview half of the /queue/[id] page (iter 5.6):
//
//	GET   /candidates/{businessId}/website                          — latest Website (no cleartext)
//	POST  /candidates/{businessId}/website/{websiteId}/approve       — status→approved; publish website.approved + feedback.captured
//	POST  /candidates/{businessId}/website/{websiteId}/reject        — status→rejected; publish website.rejected_after_review + feedback.captured
//
// Mirrors api-specs (iter 4.3) for the website subject. Regenerate-site
// / Regenerate-passcode land in iter 5.6b on this same Lambda.
//
// The GET response is deliberately sanitised: passcodeHash and
// passcodeCipher NEVER leave the BFF (cleartext stays KMS-sealed; the
// Worker validates against KV). The page only needs previewUrl,
// screenshots, status, and the cleartext-window timestamp.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/auth"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/feedback"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/httpresp"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/passcode"
)

// passcodeRevealableWindow mirrors publisher.PasscodeRevealableWindow —
// a freshly-rotated passcode is revealable for 7 days.
const passcodeRevealableWindow = 7 * 24 * time.Hour

// WebsiteRow mirrors lambdas/publisher's row shape (duplicated; sibling
// package main). Passcode hash/cipher are read but NEVER serialised out.
type WebsiteRow struct {
	PK                      string            `dynamodbav:"pk"`
	SK                      string            `dynamodbav:"sk"`
	Type                    string            `dynamodbav:"type"`
	ID                      string            `dynamodbav:"id"`
	SpecID                  string            `dynamodbav:"specId"`
	R2Prefix                string            `dynamodbav:"r2Prefix"`
	Status                  string            `dynamodbav:"status"`
	PreviewURL              string            `dynamodbav:"previewUrl,omitempty"`
	Screenshots             map[string]string `dynamodbav:"screenshots,omitempty"`
	PasscodeHash            string            `dynamodbav:"passcodeHash,omitempty"`
	PasscodeCipher          string            `dynamodbav:"passcodeCipher,omitempty"`
	PasscodeRevealableUntil int64             `dynamodbav:"passcodeRevealableUntil,omitempty"`
	PasscodeRevokedAt       string            `dynamodbav:"passcodeRevokedAt,omitempty"`
	ApprovedBy              string            `dynamodbav:"approvedBy,omitempty"`
	ApprovedAt              string            `dynamodbav:"approvedAt,omitempty"`
	CreatedAt               string            `dynamodbav:"createdAt"`
	UpdatedAt               string            `dynamodbav:"updatedAt"`
	Etag                    string            `dynamodbav:"etag"`
}

// websiteView is the sanitised projection the page consumes. No
// passcodeHash / passcodeCipher — those never leave the BFF.
type websiteView struct {
	ID                      string            `json:"id"`
	SpecID                  string            `json:"specId"`
	Status                  string            `json:"status"`
	PreviewURL              string            `json:"previewUrl,omitempty"`
	Screenshots             map[string]string `json:"screenshots,omitempty"`
	PasscodeRevealableUntil int64             `json:"passcodeRevealableUntil,omitempty"`
	PasscodeRevokedAt       string            `json:"passcodeRevokedAt,omitempty"`
	ApprovedBy              string            `json:"approvedBy,omitempty"`
	ApprovedAt              string            `json:"approvedAt,omitempty"`
	CreatedAt               string            `json:"createdAt"`
	UpdatedAt               string            `json:"updatedAt"`
}

func (w WebsiteRow) view() websiteView {
	return websiteView{
		ID:                      w.ID,
		SpecID:                  w.SpecID,
		Status:                  w.Status,
		PreviewURL:              w.PreviewURL,
		Screenshots:             w.Screenshots,
		PasscodeRevealableUntil: w.PasscodeRevealableUntil,
		PasscodeRevokedAt:       w.PasscodeRevokedAt,
		ApprovedBy:              w.ApprovedBy,
		ApprovedAt:              w.ApprovedAt,
		CreatedAt:               w.CreatedAt,
		UpdatedAt:               w.UpdatedAt,
	}
}

// BusinessRow is the subset returned alongside the website so the page
// can render the header without a second call.
type BusinessRow struct {
	ID       string `dynamodbav:"id"       json:"id"`
	Name     string `dynamodbav:"name"     json:"name"`
	Domain   string `dynamodbav:"domain"   json:"domain"`
	Vertical string `dynamodbav:"vertical" json:"vertical"`
	Location string `dynamodbav:"location" json:"location"`
	Status   string `dynamodbav:"status"   json:"status"`
}

type websiteResponse struct {
	Business BusinessRow  `json:"business"`
	Website  *websiteView `json:"website,omitempty"` // nil when no website published yet
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
	websiteID := req.PathParameters["websiteId"]

	switch {
	case method == "GET" && websiteID == "":
		return handleGetWebsite(ctx, logger, businessID)
	case method == "POST" && strings.HasSuffix(path, "/approve"):
		return handleDecision(ctx, logger, req, businessID, websiteID,
			"approved", "website.approved", feedback.ActionApprove)
	case method == "POST" && strings.HasSuffix(path, "/reject"):
		return handleDecision(ctx, logger, req, businessID, websiteID,
			"rejected", "website.rejected_after_review", feedback.ActionReject)
	case method == "POST" && strings.HasSuffix(path, "/regenerate-site"):
		return handleRegenerateSite(ctx, logger, req, businessID, websiteID)
	case method == "POST" && strings.HasSuffix(path, "/regenerate-passcode"):
		return handleRegeneratePasscode(ctx, logger, req, businessID, websiteID)
	default:
		return httpresp.Error(405, "method not allowed"), nil
	}
}

// --- GET /candidates/{businessId}/website ------------------------------

func handleGetWebsite(ctx context.Context, logger *slog.Logger, businessID string) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" {
		return httpresp.Error(400, "businessId is required"), nil
	}
	biz, err := getBusiness(ctx, businessID)
	if err != nil {
		logger.Error("api-website.getBusiness failed", "err", err)
		return httpresp.Error(500, "could not load business"), nil
	}
	if biz == nil {
		return httpresp.Error(404, "business not found"), nil
	}
	latest, err := latestWebsite(ctx, businessID)
	if err != nil {
		logger.Error("api-website.latestWebsite failed", "err", err)
		return httpresp.Error(500, "could not load website"), nil
	}
	resp := websiteResponse{Business: *biz}
	if latest != nil {
		v := latest.view()
		resp.Website = &v
	}
	body, _ := json.Marshal(resp)
	return httpresp.JSON(200, string(body)), nil
}

// --- POST .../{websiteId}/approve | /reject ----------------------------

type approveRejectBody struct {
	Notes string `json:"notes,omitempty"`
}

// handleDecision flips the Website status, stamps approvedBy/At,
// publishes website.<approved|rejected_after_review>, captures feedback.
// Only a `published` preview (published + screenshotted, awaiting
// review) can be decided on.
func handleDecision(
	ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest,
	businessID, websiteID, newStatus, eventName, feedbackAction string,
) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" || websiteID == "" {
		return httpresp.Error(400, "businessId and websiteId are required"), nil
	}
	var body approveRejectBody
	if req.Body != "" {
		if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
			return httpresp.Error(400, fmt.Sprintf("invalid JSON body: %v", err)), nil
		}
	}

	current, err := getWebsite(ctx, businessID, websiteID)
	if err != nil {
		logger.Error("api-website.getWebsite failed", "err", err)
		return httpresp.Error(500, "could not load website"), nil
	}
	if current == nil {
		return httpresp.Error(404, "website not found"), nil
	}
	if current.Status != "published" {
		return httpresp.Error(409, fmt.Sprintf("website is %q; can only decide on a published preview", current.Status)), nil
	}

	actor := auth.Sub(req)
	now := nowFunc().UTC().Format(time.RFC3339)

	updated := *current
	updated.Status = newStatus
	updated.ApprovedBy = actor
	updated.ApprovedAt = now
	updated.UpdatedAt = now
	updated.Etag = randomHexFn(16)
	if err := putWebsite(ctx, updated); err != nil {
		logger.Error("api-website.putWebsite failed", "err", err)
		return httpresp.Error(500, "could not save website"), nil
	}

	if err := publishWebsiteDecision(ctx, eventName, businessID, websiteID); err != nil {
		logger.Error("api-website.publish failed", "err", err)
		return httpresp.Error(502, "could not publish event"), nil
	}

	// originalPayload is the sanitised view — no passcode material ever
	// reaches the feedback log.
	originalJSON, _ := json.Marshal(current.view())
	if err := captureFeedback(ctx, feedback.CaptureInput{
		Subject:         feedback.SubjectWebsite,
		SubjectID:       websiteID,
		BusinessID:      businessID,
		Actor:           actor,
		Action:          feedbackAction,
		OriginalPayload: string(originalJSON),
		Notes:           body.Notes,
		Vertical:        verticalFor(ctx, businessID),
	}); err != nil {
		logger.Error("api-website.feedback.capture failed", "err", err)
	}

	out, _ := json.Marshal(updated.view())
	return httpresp.JSON(200, string(out)), nil
}

// --- POST .../{websiteId}/regenerate-site -----------------------------
//
// Operator asks for a fresh render. We emit website.regenerate.requested
// {businessId, specId, websiteId}; the generator re-renders the existing
// approved Spec (no Bedrock) onto the SAME websiteId, and the publisher
// then overwrites the R2 prefix + KV key — invalidating the old passcode
// and issuing a fresh one. Status is parked at "regenerated" until the
// publisher flips it back to "published".

func handleRegenerateSite(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest, businessID, websiteID string) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" || websiteID == "" {
		return httpresp.Error(400, "businessId and websiteId are required"), nil
	}
	var body approveRejectBody
	if req.Body != "" {
		if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
			return httpresp.Error(400, fmt.Sprintf("invalid JSON body: %v", err)), nil
		}
	}
	current, err := getWebsite(ctx, businessID, websiteID)
	if err != nil {
		logger.Error("api-website.getWebsite failed", "err", err)
		return httpresp.Error(500, "could not load website"), nil
	}
	if current == nil {
		return httpresp.Error(404, "website not found"), nil
	}
	if current.SpecID == "" {
		return httpresp.Error(409, "website has no spec to re-render"), nil
	}

	actor := auth.Sub(req)
	if err := publishRegenerateRequested(ctx, businessID, current.SpecID, websiteID); err != nil {
		logger.Error("api-website.publish regenerate failed", "err", err)
		return httpresp.Error(502, "could not publish event"), nil
	}

	now := nowFunc().UTC().Format(time.RFC3339)
	updated := *current
	updated.Status = "regenerated"
	updated.UpdatedAt = now
	updated.Etag = randomHexFn(16)
	if err := putWebsite(ctx, updated); err != nil {
		logger.Error("api-website.putWebsite failed", "err", err)
		return httpresp.Error(500, "could not save website"), nil
	}

	originalJSON, _ := json.Marshal(current.view())
	if err := captureFeedback(ctx, feedback.CaptureInput{
		Subject:         feedback.SubjectWebsite,
		SubjectID:       websiteID,
		BusinessID:      businessID,
		Actor:           actor,
		Action:          feedback.ActionRegenerate,
		OriginalPayload: string(originalJSON),
		Notes:           body.Notes,
		Vertical:        verticalFor(ctx, businessID),
	}); err != nil {
		logger.Error("api-website.feedback.capture failed", "err", err)
	}

	out, _ := json.Marshal(updated.view())
	return httpresp.JSON(200, string(out)), nil
}

// --- POST .../{websiteId}/regenerate-passcode -------------------------
//
// Rotate the passcode WITHOUT re-rendering. New code → hash → KV
// overwrite (old hash replaced, so the old cleartext is instantly
// invalid) → KMS-encrypt → Website row. The cleartext lives only in
// `code`; it is hashed/encrypted before any logger or response and is
// NEVER logged, returned, or put in an event.

func handleRegeneratePasscode(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest, businessID, websiteID string) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" || websiteID == "" {
		return httpresp.Error(400, "businessId and websiteId are required"), nil
	}
	var body approveRejectBody
	if req.Body != "" {
		if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
			return httpresp.Error(400, fmt.Sprintf("invalid JSON body: %v", err)), nil
		}
	}
	current, err := getWebsite(ctx, businessID, websiteID)
	if err != nil {
		logger.Error("api-website.getWebsite failed", "err", err)
		return httpresp.Error(500, "could not load website"), nil
	}
	if current == nil {
		return httpresp.Error(404, "website not found"), nil
	}

	ops, err := passcodeOpsProvider(ctx)
	if err != nil {
		logger.Error("api-website.passcodeOps failed", "err", err)
		return httpresp.Error(500, "passcode subsystem unavailable"), nil
	}
	// NEVER LOG `code`. Hashed + KMS-sealed before anything observable.
	code, err := ops.Gen()
	if err != nil {
		logger.Error("api-website.passcode.generate failed", "err", err)
		return httpresp.Error(500, "could not generate passcode"), nil
	}
	hash := ops.Hash(code, ops.Salt)
	cipher, err := ops.Encrypt(ctx, code)
	if err != nil {
		logger.Error("api-website.passcode.encrypt failed", "err", err)
		return httpresp.Error(500, "could not seal passcode"), nil
	}
	if err := ops.KVPut(ctx, "passcode:"+websiteID, hash, map[string]string{
		"businessId": businessID, "websiteId": websiteID,
	}); err != nil {
		logger.Error("api-website.passcode.kv failed", "err", err)
		return httpresp.Error(502, "could not write passcode to KV"), nil
	}

	actor := auth.Sub(req)
	now := nowFunc().UTC()
	originalJSON, _ := json.Marshal(current.view())

	updated := *current
	updated.PasscodeHash = hash
	updated.PasscodeCipher = cipher
	updated.PasscodeRevealableUntil = now.Add(passcodeRevealableWindow).Unix()
	updated.PasscodeRevokedAt = "" // a fresh code clears any prior revocation
	updated.UpdatedAt = now.Format(time.RFC3339)
	updated.Etag = randomHexFn(16)
	if err := putWebsite(ctx, updated); err != nil {
		logger.Error("api-website.putWebsite failed", "err", err)
		return httpresp.Error(500, "could not save website"), nil
	}

	if err := captureFeedback(ctx, feedback.CaptureInput{
		Subject:         feedback.SubjectWebsite,
		SubjectID:       websiteID,
		BusinessID:      businessID,
		Actor:           actor,
		Action:          feedback.ActionRegenerate,
		OriginalPayload: string(originalJSON),
		Notes:           body.Notes,
		Vertical:        verticalFor(ctx, businessID),
	}); err != nil {
		logger.Error("api-website.feedback.capture failed", "err", err)
	}

	logger.Info("api-website.passcode.regenerated", "websiteId", websiteID) // no cleartext
	out, _ := json.Marshal(updated.view())
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

func getWebsite(ctx context.Context, businessID, websiteID string) (*WebsiteRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "WEBSITE#" + websiteID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get website: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var w WebsiteRow
	if err := attributevalue.UnmarshalMap(out.Item, &w); err != nil {
		return nil, fmt.Errorf("unmarshal website: %w", err)
	}
	return &w, nil
}

// latestWebsite returns the newest Website row for a business, or nil.
func latestWebsite(ctx context.Context, businessID string) (*WebsiteRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk":     &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			":prefix": &dtypes.AttributeValueMemberS{Value: "WEBSITE#"},
		},
		ScanIndexForward: aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("query websites: %w", err)
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	var w WebsiteRow
	if err := attributevalue.UnmarshalMap(out.Items[0], &w); err != nil {
		return nil, fmt.Errorf("unmarshal website row: %w", err)
	}
	return &w, nil
}

func putWebsite(ctx context.Context, row WebsiteRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal website: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put website: %w", err)
	}
	return nil
}

func verticalFor(ctx context.Context, businessID string) string {
	biz, err := getBusiness(ctx, businessID)
	if err != nil || biz == nil {
		return ""
	}
	return biz.Vertical
}

// publishWebsiteDecision emits website.approved or
// website.rejected_after_review. Shape `{businessId, websiteId}` —
// consistent with the spec-decision events.
func publishWebsiteDecision(ctx context.Context, eventName, businessID, websiteID string) error {
	publisher, err := publisherProvider(ctx)
	if err != nil {
		return fmt.Errorf("api-website: publisher: %w", err)
	}
	env := pkgevents.New(eventName, "api-website", map[string]any{
		"businessId": businessID,
		"websiteId":  websiteID,
	})
	return pkgevents.Publish(ctx, publisher, env)
}

// publishRegenerateRequested emits website.regenerate.requested. The
// generator consumes it and re-renders onto the same websiteId.
func publishRegenerateRequested(ctx context.Context, businessID, specID, websiteID string) error {
	publisher, err := publisherProvider(ctx)
	if err != nil {
		return fmt.Errorf("api-website: publisher: %w", err)
	}
	env := pkgevents.New("website.regenerate.requested", "api-website", map[string]any{
		"businessId": businessID,
		"specId":     specID,
		"websiteId":  websiteID,
	})
	return pkgevents.Publish(ctx, publisher, env)
}

func captureFeedback(ctx context.Context, in feedback.CaptureInput) error {
	publisher, err := publisherProvider(ctx)
	if err != nil {
		return fmt.Errorf("api-website: publisher: %w", err)
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

func defaultRandomHex(_ int) string {
	return fmt.Sprintf("%016x", time.Now().UnixNano())
}

// --- passcode rotation wiring (regenerate-passcode) -------------------
//
// passcodeOps is the injectable surface for the rotate path so tests
// never touch real KMS / Cloudflare KV. Production wiring mirrors the
// publisher Lambda (iter 5.3).

type passcodeOps struct {
	Gen     func() (string, error)
	Hash    func(code, salt string) string
	Encrypt func(ctx context.Context, cleartext string) (string, error)
	KVPut   func(ctx context.Context, key, value string, meta map[string]string) error
	Salt    string
}

var passcodeOpsProvider = defaultPasscodeOps

func defaultPasscodeOps(ctx context.Context) (*passcodeOps, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("api-website: AWS config: %w", err)
	}
	salt := os.Getenv("PASSCODE_SALT")
	kmsKeyID := os.Getenv("PASSCODE_KMS_KEY_ID")
	cfAccountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	cfKVNamespace := os.Getenv("CLOUDFLARE_KV_NAMESPACE_ID")
	cfAPIToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	if salt == "" || kmsKeyID == "" || cfAccountID == "" || cfKVNamespace == "" || cfAPIToken == "" {
		return nil, errors.New("api-website: PASSCODE_SALT / PASSCODE_KMS_KEY_ID / CLOUDFLARE_* env vars are not set")
	}
	kmsClient := kms.NewFromConfig(cfg)
	kv := passcode.NewKVWriter(cfAccountID, cfKVNamespace, cfAPIToken)
	return &passcodeOps{
		Gen:  passcode.Generate,
		Hash: passcode.Hash,
		Encrypt: func(ctx context.Context, cleartext string) (string, error) {
			return passcode.EncryptCleartext(ctx, kmsClient, kmsKeyID, cleartext)
		},
		KVPut: kv.Put,
		Salt:  salt,
	}, nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
