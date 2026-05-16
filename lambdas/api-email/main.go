// Package main is the api-email Lambda. Operator-only BFF routes for
// the email-review page (iter 7.3) at /queue/[id]/email:
//
//	GET   /candidates/{businessId}/email                       — latest EmailDraft (operator sees the real body)
//	PATCH /candidates/{businessId}/email/{emailId}             — edit subject/body; feedback.captured(edit)
//	POST  /candidates/{businessId}/email/{emailId}/approve     — status→approved; publish email.approved + feedback
//	POST  /candidates/{businessId}/email/{emailId}/reject      — status→rejected; publish email.rejected + feedback
//
// Mirrors api-specs (iter 4.3) / api-website (iter 5.6a) for the email
// subject.
//
// CLEARTEXT: the EmailDraft.body persisted by iter 7.2b contains the
// real passcode (a sanctioned at-rest location per 02-data-model.md).
// The operator is authorised to see it on this review page (they are
// the sender). But the `feedback.captured` row + event must NEVER carry
// cleartext (10-quality-rules § Rule 2): every OriginalPayload /
// EditedPayload is redacted by KMS-decrypting Website.passcodeCipher
// and replacing the cleartext with the {{PASSCODE}} placeholder. If the
// cipher is already wiped (post-send / >7d) the code can't be recovered
// precisely, so the redactor blanks the body in the feedback payload
// rather than risk a leak.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/auth"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/feedback"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/httpresp"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/passcode"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// EmailDraftRow mirrors 02-data-model.md § "EmailDraft". The Body holds
// the real passcode at rest — never serialised into a feedback row.
type EmailDraftRow struct {
	PK         string `dynamodbav:"pk" json:"-"`
	SK         string `dynamodbav:"sk" json:"-"`
	Type       string `dynamodbav:"type" json:"-"`
	ID         string `dynamodbav:"id" json:"id"`
	WebsiteID  string `dynamodbav:"websiteId" json:"websiteId"`
	ContactID  string `dynamodbav:"contactId,omitempty" json:"contactId,omitempty"`
	Subject    string `dynamodbav:"subject" json:"subject"`
	Body       string `dynamodbav:"body" json:"body"`
	OptOutLine string `dynamodbav:"optOutLine" json:"optOutLine"`
	WordCount  int    `dynamodbav:"wordCount" json:"wordCount"`
	ModelID    string `dynamodbav:"modelId" json:"modelId"`
	PromptID   string `dynamodbav:"promptId" json:"promptId"`
	Status     string `dynamodbav:"status" json:"status"`
	ApprovedBy string `dynamodbav:"approvedBy,omitempty" json:"approvedBy,omitempty"`
	ApprovedAt string `dynamodbav:"approvedAt,omitempty" json:"approvedAt,omitempty"`
	CreatedAt  string `dynamodbav:"createdAt" json:"createdAt"`
	UpdatedAt  string `dynamodbav:"updatedAt" json:"updatedAt"`
	Etag       string `dynamodbav:"etag" json:"etag"`
}

type BusinessRow struct {
	ID       string `dynamodbav:"id"       json:"id"`
	Name     string `dynamodbav:"name"     json:"name"`
	Domain   string `dynamodbav:"domain"   json:"domain"`
	Vertical string `dynamodbav:"vertical" json:"vertical"`
	Location string `dynamodbav:"location" json:"location"`
	Status   string `dynamodbav:"status"   json:"status"`
}

type WebsiteRow struct {
	ID             string `dynamodbav:"id"`
	PasscodeCipher string `dynamodbav:"passcodeCipher,omitempty"`
}

type emailResponse struct {
	Business BusinessRow    `json:"business"`
	Email    *EmailDraftRow `json:"email,omitempty"` // body is the REAL draft — operator-authorised view
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
	emailID := req.PathParameters["emailId"]

	switch {
	case method == "GET" && businessID == "" && strings.HasSuffix(path, "/email/status"):
		return handleEmailStatus(ctx, logger)
	case method == "GET" && emailID == "":
		return handleGetEmail(ctx, logger, businessID)
	case method == "PATCH" && emailID != "":
		return handleEditEmail(ctx, logger, req, businessID, emailID)
	case method == "POST" && strings.HasSuffix(path, "/approve"):
		return handleDecision(ctx, logger, req, businessID, emailID,
			"approved", "email.approved", feedback.ActionApprove)
	case method == "POST" && strings.HasSuffix(path, "/reject"):
		return handleDecision(ctx, logger, req, businessID, emailID,
			"rejected", "email.rejected", feedback.ActionReject)
	default:
		return httpresp.Error(405, "method not allowed"), nil
	}
}

// --- GET ---------------------------------------------------------------

func handleGetEmail(ctx context.Context, logger *slog.Logger, businessID string) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" {
		return httpresp.Error(400, "businessId is required"), nil
	}
	biz, err := getBusiness(ctx, businessID)
	if err != nil {
		logger.Error("api-email.getBusiness failed", "err", err)
		return httpresp.Error(500, "could not load business"), nil
	}
	if biz == nil {
		return httpresp.Error(404, "business not found"), nil
	}
	latest, err := latestDraft(ctx, businessID)
	if err != nil {
		logger.Error("api-email.latestDraft failed", "err", err)
		return httpresp.Error(500, "could not load email draft"), nil
	}
	resp := emailResponse{Business: *biz, Email: latest}
	body, _ := json.Marshal(resp)
	return httpresp.JSON(200, string(body)), nil
}

// --- GET /email/status (iter 8.1) -------------------------------------
//
// Surfaces SES domain-identity verification status for the operator's
// /settings/email panel ("DKIM/SPF checks live" per 08-admin-ui.md).
// Read-only; no SES send. The identity is the project-global
// outreach.<base_domain> singleton (terraform/ses.tf, prod-only).

type emailStatusResponse struct {
	Identity             string `json:"identity"`
	VerifiedForSending   bool   `json:"verifiedForSending"`
	DKIMStatus           string `json:"dkimStatus"`
	DKIMSigningEnabled   bool   `json:"dkimSigningEnabled"`
	MailFromDomain       string `json:"mailFromDomain,omitempty"`
	MailFromDomainStatus string `json:"mailFromDomainStatus,omitempty"`
}

func handleEmailStatus(ctx context.Context, logger *slog.Logger) (events.APIGatewayV2HTTPResponse, error) {
	identity := os.Getenv("SES_OUTREACH_IDENTITY")
	if identity == "" {
		return httpresp.Error(500, "SES_OUTREACH_IDENTITY is not set"), nil
	}
	status, err := sesStatusProvider(ctx, identity)
	if err != nil {
		// SES identity is a prod-only singleton — in a per-PR env it
		// won't exist. Report that plainly rather than 5xx-ing.
		logger.Info("api-email.ses_status.unavailable", "err", err.Error())
		return httpresp.JSON(200, mustJSON(emailStatusResponse{
			Identity:           identity,
			VerifiedForSending: false,
			DKIMStatus:         "UNKNOWN",
		})), nil
	}
	return httpresp.JSON(200, mustJSON(status)), nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// --- PATCH (edit) ------------------------------------------------------

type patchEmailBody struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Notes   string `json:"notes,omitempty"`
}

func handleEditEmail(ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest, businessID, emailID string) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" || emailID == "" {
		return httpresp.Error(400, "businessId and emailId are required"), nil
	}
	var body patchEmailBody
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		return httpresp.Error(400, fmt.Sprintf("invalid JSON body: %v", err)), nil
	}
	if strings.TrimSpace(body.Subject) == "" || strings.TrimSpace(body.Body) == "" {
		return httpresp.Error(400, "subject and body are required"), nil
	}

	current, err := getDraft(ctx, businessID, emailID)
	if err != nil {
		logger.Error("api-email.getDraft failed", "err", err)
		return httpresp.Error(500, "could not load email draft"), nil
	}
	if current == nil {
		return httpresp.Error(404, "email draft not found"), nil
	}
	if current.Status != "draft" {
		return httpresp.Error(409, fmt.Sprintf("email is %q; can only edit a draft", current.Status)), nil
	}

	code := redactionCode(ctx, businessID, current.WebsiteID, logger)

	updated := *current
	updated.Subject = body.Subject
	updated.Body = body.Body
	updated.UpdatedAt = nowFunc().UTC().Format(time.RFC3339)
	updated.Etag = randomHexFn(16)
	if err := putDraft(ctx, updated); err != nil {
		logger.Error("api-email.putDraft failed", "err", err)
		return httpresp.Error(500, "could not save email draft"), nil
	}

	if err := captureFeedback(ctx, feedback.CaptureInput{
		Subject:         feedback.SubjectEmail,
		SubjectID:       emailID,
		BusinessID:      businessID,
		Actor:           auth.Sub(req),
		Action:          feedback.ActionEdit,
		OriginalPayload: redactedJSON(current, code),
		EditedPayload:   redactedJSON(&updated, code),
		Notes:           body.Notes,
		Vertical:        verticalFor(ctx, businessID),
	}); err != nil {
		logger.Error("api-email.feedback.capture failed", "err", err)
	}

	out, _ := json.Marshal(updated)
	return httpresp.JSON(200, string(out)), nil
}

// --- POST approve | reject --------------------------------------------

type approveRejectBody struct {
	Notes string `json:"notes,omitempty"`
}

func handleDecision(
	ctx context.Context, logger *slog.Logger, req events.APIGatewayV2HTTPRequest,
	businessID, emailID, newStatus, eventName, feedbackAction string,
) (events.APIGatewayV2HTTPResponse, error) {
	if businessID == "" || emailID == "" {
		return httpresp.Error(400, "businessId and emailId are required"), nil
	}
	var body approveRejectBody
	if req.Body != "" {
		if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
			return httpresp.Error(400, fmt.Sprintf("invalid JSON body: %v", err)), nil
		}
	}

	current, err := getDraft(ctx, businessID, emailID)
	if err != nil {
		logger.Error("api-email.getDraft failed", "err", err)
		return httpresp.Error(500, "could not load email draft"), nil
	}
	if current == nil {
		return httpresp.Error(404, "email draft not found"), nil
	}
	if current.Status != "draft" {
		return httpresp.Error(409, fmt.Sprintf("email is %q; can only decide on a draft", current.Status)), nil
	}

	actor := auth.Sub(req)
	now := nowFunc().UTC().Format(time.RFC3339)
	code := redactionCode(ctx, businessID, current.WebsiteID, logger)

	updated := *current
	updated.Status = newStatus
	updated.ApprovedBy = actor
	updated.ApprovedAt = now
	updated.UpdatedAt = now
	updated.Etag = randomHexFn(16)
	if err := putDraft(ctx, updated); err != nil {
		logger.Error("api-email.putDraft failed", "err", err)
		return httpresp.Error(500, "could not save email draft"), nil
	}

	if err := publishEmailDecision(ctx, eventName, businessID, emailID, current.WebsiteID, current.ContactID); err != nil {
		logger.Error("api-email.publish failed", "err", err)
		return httpresp.Error(502, "could not publish event"), nil
	}

	if err := captureFeedback(ctx, feedback.CaptureInput{
		Subject:         feedback.SubjectEmail,
		SubjectID:       emailID,
		BusinessID:      businessID,
		Actor:           actor,
		Action:          feedbackAction,
		OriginalPayload: redactedJSON(current, code),
		Notes:           body.Notes,
		Vertical:        verticalFor(ctx, businessID),
	}); err != nil {
		logger.Error("api-email.feedback.capture failed", "err", err)
	}

	out, _ := json.Marshal(updated)
	return httpresp.JSON(200, string(out)), nil
}

// --- redaction ---------------------------------------------------------

// redactionCode returns the cleartext passcode (via KMS-decrypt of the
// Website.passcodeCipher) used to scrub the body before it reaches the
// feedback log. Empty when the cipher is wiped/unavailable — callers
// then fall back to blanking the body (redactedJSON).
func redactionCode(ctx context.Context, businessID, websiteID string, logger *slog.Logger) string {
	if websiteID == "" {
		return ""
	}
	web, err := getWebsite(ctx, businessID, websiteID)
	if err != nil || web == nil || web.PasscodeCipher == "" {
		return ""
	}
	dec, err := decryptProvider(ctx)
	if err != nil {
		logger.Warn("api-email.redaction.kms_unavailable", "err", err)
		return ""
	}
	code, err := dec(ctx, web.PasscodeCipher)
	if err != nil {
		logger.Warn("api-email.redaction.decrypt_failed") // never log the cipher/code
		return ""
	}
	return code
}

// redactedJSON marshals an EmailDraft for the feedback log with the
// cleartext passcode replaced by the {{PASSCODE}} placeholder. If the
// code couldn't be recovered, the body + subject are blanked so a
// cleartext value can NEVER reach the feedback row (fail-safe).
func redactedJSON(d *EmailDraftRow, code string) string {
	red := *d
	if code != "" {
		red.Body = strings.ReplaceAll(red.Body, code, schemas.PasscodePlaceholder)
		red.Subject = strings.ReplaceAll(red.Subject, code, schemas.PasscodePlaceholder)
	} else {
		red.Body = "[redacted — passcode unrecoverable]"
		red.Subject = "[redacted]"
	}
	b, _ := json.Marshal(red)
	return string(b)
}

// --- DDB helpers -------------------------------------------------------

func getBusiness(ctx context.Context, businessID string) (*BusinessRow, error) {
	item, err := getItem(ctx, "BUSINESS#"+businessID, "PROFILE")
	if err != nil || item == nil {
		return nil, err
	}
	var b BusinessRow
	if err := attributevalue.UnmarshalMap(item, &b); err != nil {
		return nil, fmt.Errorf("unmarshal business: %w", err)
	}
	return &b, nil
}

func getDraft(ctx context.Context, businessID, emailID string) (*EmailDraftRow, error) {
	item, err := getItem(ctx, "BUSINESS#"+businessID, "EMAIL_DRAFT#"+emailID)
	if err != nil || item == nil {
		return nil, err
	}
	var d EmailDraftRow
	if err := attributevalue.UnmarshalMap(item, &d); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	return &d, nil
}

func getWebsite(ctx context.Context, businessID, websiteID string) (*WebsiteRow, error) {
	item, err := getItem(ctx, "BUSINESS#"+businessID, "WEBSITE#"+websiteID)
	if err != nil || item == nil {
		return nil, err
	}
	var w WebsiteRow
	if err := attributevalue.UnmarshalMap(item, &w); err != nil {
		return nil, fmt.Errorf("unmarshal website: %w", err)
	}
	return &w, nil
}

func getItem(ctx context.Context, pk, sk string) (map[string]dtypes.AttributeValue, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: pk},
			"sk": &dtypes.AttributeValueMemberS{Value: sk},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", pk, sk, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	return out.Item, nil
}

// latestDraft returns the newest EmailDraft for a business (lexically
// highest SK; ids are random but "latest" only needs one consistent
// pick for the review page — the iter-7.2b Lambda writes one per
// approval).
func latestDraft(ctx context.Context, businessID string) (*EmailDraftRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk":     &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			":prefix": &dtypes.AttributeValueMemberS{Value: "EMAIL_DRAFT#"},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("query drafts: %w", err)
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	var d EmailDraftRow
	if err := attributevalue.UnmarshalMap(out.Items[0], &d); err != nil {
		return nil, fmt.Errorf("unmarshal draft row: %w", err)
	}
	return &d, nil
}

func putDraft(ctx context.Context, row EmailDraftRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal draft: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put draft %s: %w", row.ID, err)
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

func publishEmailDecision(ctx context.Context, eventName, businessID, emailID, websiteID, contactID string) error {
	publisher, err := publisherProvider(ctx)
	if err != nil {
		return fmt.Errorf("api-email: publisher: %w", err)
	}
	env := pkgevents.New(eventName, "api-email", map[string]any{
		"businessId": businessID,
		"draftId":    emailID,
		"websiteId":  websiteID,
		"contactId":  contactID,
	})
	return pkgevents.Publish(ctx, publisher, env)
}

func captureFeedback(ctx context.Context, in feedback.CaptureInput) error {
	publisher, err := publisherProvider(ctx)
	if err != nil {
		return fmt.Errorf("api-email: publisher: %w", err)
	}
	_, _, err = feedback.Capture(ctx, in, publisher)
	return err
}

// --- AWS wiring (lazy) -------------------------------------------------

var (
	cachedPublisher   *pkgevents.Publisher
	nowFunc           = func() time.Time { return time.Now().UTC() }
	randomHexFn       = defaultRandomHex
	decryptProvider   = defaultDecryptProvider
	sesStatusProvider = defaultSESStatus
)

// defaultSESStatus calls SESv2 GetEmailIdentity for the outreach
// domain singleton and projects the verification + DKIM + MAIL FROM
// status. Read-only (sesv2:GetEmailIdentity).
func defaultSESStatus(ctx context.Context, identity string) (emailStatusResponse, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return emailStatusResponse{}, fmt.Errorf("api-email: AWS config: %w", err)
	}
	out, err := sesv2.NewFromConfig(cfg).GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{
		EmailIdentity: aws.String(identity),
	})
	if err != nil {
		return emailStatusResponse{}, fmt.Errorf("api-email: GetEmailIdentity: %w", err)
	}
	resp := emailStatusResponse{
		Identity:           identity,
		VerifiedForSending: out.VerifiedForSendingStatus,
		DKIMStatus:         "UNKNOWN",
	}
	if out.DkimAttributes != nil {
		resp.DKIMStatus = string(out.DkimAttributes.Status)
		resp.DKIMSigningEnabled = out.DkimAttributes.SigningEnabled
	}
	if out.MailFromAttributes != nil {
		resp.MailFromDomain = aws.ToString(out.MailFromAttributes.MailFromDomain)
		resp.MailFromDomainStatus = string(out.MailFromAttributes.MailFromDomainStatus)
	}
	return resp, nil
}

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

// defaultDecryptProvider builds the KMS-backed decrypt fn. Only KMS is
// needed (redaction never re-encrypts / touches KV), so this is leaner
// than api-website's passcodeOps.
func defaultDecryptProvider(ctx context.Context) (func(context.Context, string) (string, error), error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("api-email: AWS config: %w", err)
	}
	if os.Getenv("PASSCODE_KMS_KEY_ID") == "" {
		return nil, fmt.Errorf("api-email: PASSCODE_KMS_KEY_ID is not set")
	}
	kmsClient := kms.NewFromConfig(cfg)
	return func(ctx context.Context, ct string) (string, error) {
		return passcode.DecryptCleartext(ctx, kmsClient, ct)
	}, nil
}

func defaultRandomHex(_ int) string {
	return fmt.Sprintf("%016x", time.Now().UnixNano())
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
