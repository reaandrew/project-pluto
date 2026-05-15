// Package main is the email-draft Lambda. EventBridge routes
// `website.approved` events here via an SQS main queue (with DLQ).
//
// Note on the trigger: .ralph/specs/09-iterations.md § Iteration 7
// names the trigger "outreach.email.requested", but the authoritative
// routing table in 03-events.md (and the actual producer, the
// api-website approve handler from iter 5.6a) emit `website.approved`
// → email-draft. We consume `website.approved`; this comment records
// the reconciliation since .ralph/specs/ is read-only here.
//
// Per-record pipeline:
//
//  1. Decode the envelope; idempotency.WithIdempotency on env.EventID.
//  2. Load Business + the approved Website + latest Audit + latest
//     Contact; load the vertical EmailToneProfile (→ "default").
//  3. If Website.passcodeCipher is empty (already wiped), log a clear
//     "regenerate passcode first" error and create NO draft (terminal
//     — retrying won't conjure the cipher).
//  4. KMS-decrypt passcodeCipher → cleartext (process memory only).
//  5. emaildraft.Run → EmailV1 (Haiku 4.5) with the {{PASSCODE}}
//     placeholder; cache key uses the passcode-free
//     (business,website,contact,tone.version) hash.
//  6. Substitute the cleartext for {{PASSCODE}} (the ONLY place
//     cleartext touches the body; it then lives at rest only in
//     EmailDraft.body, per 02-data-model.md).
//  7. Persist the EmailDraft row (status="draft").
//  8. Publish `outreach.email_ready` (NO body, NO cleartext).
//
// Entry-level killswitch wraps StageOutreach. The cleartext passcode is
// NEVER logged, traced, or put in an event (10-quality-rules § Rule 2).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/emaildraft"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/passcode"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/tone"
)

const consumerName = "email-draft"

// WebsiteApprovedDetail mirrors api-website's published shape (iter
// 5.6a publishWebsiteDecision): {businessId, websiteId}.
type WebsiteApprovedDetail struct {
	BusinessID string `json:"businessId"`
	WebsiteID  string `json:"websiteId"`
}

// EmailReadyDetail is the `outreach.email_ready` payload per
// 03-events.md. NEVER carries the body or the cleartext.
type EmailReadyDetail struct {
	BusinessID string `json:"businessId"`
	DraftID    string `json:"draftId"`
	WebsiteID  string `json:"websiteId"`
	ContactID  string `json:"contactId,omitempty"`
	WordCount  int    `json:"wordCount"`
}

// runDeps is the testable surface.
type runDeps struct {
	GetBusiness      func(ctx context.Context, businessID string) (*BusinessRow, error)
	GetWebsite       func(ctx context.Context, businessID, websiteID string) (*WebsiteRow, error)
	GetLatestAudit   func(ctx context.Context, businessID string) (*AuditRow, error)
	GetLatestContact func(ctx context.Context, businessID string) (*ContactRow, error)
	GetTone          func(ctx context.Context, vertical string) (tone.Profile, error)
	Decrypt          func(ctx context.Context, ciphertextB64 string) (string, error)
	RunDraft         func(ctx context.Context, in emaildraft.Input, capUSD float64) (schemas.EmailV1, error)
	PutDraft         func(ctx context.Context, row EmailDraftRow) error
	Publish          func(ctx context.Context, env pkgevents.Envelope[EmailReadyDetail]) error
	CapUSD           func(ctx context.Context, stage string) (float64, error)
	Now              func() time.Time
	NewDraftID       func() string
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	err := killswitch.WithKillSwitch(ctx, killswitch.StageOutreach, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		out, err := pkgevents.Consume[WebsiteApprovedDetail](ctx, raw, func(ctx context.Context, env pkgevents.Envelope[WebsiteApprovedDetail]) error {
			return processRecord(ctx, deps, env)
		})
		resp = out
		return err
	})
	return resp, err
}

func processRecord(ctx context.Context, d runDeps, env pkgevents.Envelope[WebsiteApprovedDetail]) error {
	logger := applog.FromContext(ctx).With(
		"eventId", env.EventID,
		"businessId", env.Detail.BusinessID,
		"websiteId", env.Detail.WebsiteID,
	)
	_, err := idempotency.WithIdempotency(ctx, consumerName, env.EventID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, runOne(ctx, d, env, logger)
	})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("email-draft.replay.skipped")
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, env pkgevents.Envelope[WebsiteApprovedDetail], logger *slog.Logger) error {
	biz, err := d.GetBusiness(ctx, env.Detail.BusinessID)
	if err != nil {
		return fmt.Errorf("email-draft: get business: %w", err)
	}
	if biz == nil {
		logger.Warn("email-draft.business.missing")
		return nil
	}
	web, err := d.GetWebsite(ctx, env.Detail.BusinessID, env.Detail.WebsiteID)
	if err != nil {
		return fmt.Errorf("email-draft: get website: %w", err)
	}
	if web == nil {
		logger.Warn("email-draft.website.missing")
		return nil
	}
	// Terminal guard: a wiped cipher can't be recovered by retrying.
	// Clear operator-actionable signal; create no draft, no DLQ churn.
	if web.PasscodeCipher == "" {
		logger.Error("email-draft.passcode.wiped",
			"detail", "Website.passcodeCipher is empty — regenerate the passcode before drafting; no draft created")
		return nil
	}

	audit, err := d.GetLatestAudit(ctx, env.Detail.BusinessID)
	if err != nil {
		return fmt.Errorf("email-draft: get audit: %w", err)
	}
	contact, err := d.GetLatestContact(ctx, env.Detail.BusinessID)
	if err != nil {
		return fmt.Errorf("email-draft: get contact: %w", err)
	}
	profile, err := d.GetTone(ctx, biz.Vertical)
	if err != nil {
		return fmt.Errorf("email-draft: get tone profile: %w", err)
	}

	// Cleartext lives in `code` only; substituted into the body after
	// validation and never logged/evented.
	code, err := d.Decrypt(ctx, web.PasscodeCipher)
	if err != nil {
		return fmt.Errorf("email-draft: kms decrypt passcode: %w", err)
	}

	capUSD, err := d.CapUSD(ctx, killswitch.StageOutreach)
	if err != nil {
		return fmt.Errorf("email-draft: lookup email budget: %w", err)
	}

	in := emaildraft.Input{
		BusinessID: biz.ID,
		WebsiteID:  web.ID,
		PreviewURL: web.PreviewURL,
		Business: emaildraft.Business{
			Name: biz.Name, Domain: biz.Domain,
			Vertical: biz.Vertical, Location: biz.Location,
		},
		Audit: emaildraft.AuditSummary{},
		Tone:  profile,
	}
	if audit != nil {
		in.Audit = emaildraft.AuditSummary{Score: audit.Score, Summary: auditSummary(audit)}
	}
	contactID := ""
	if contact != nil {
		contactID = contact.ID
		in.ContactID = contact.ID
		in.Contact = emaildraft.Contact{FirstName: firstName(contact.Name), Role: contact.Role}
	}

	draft, err := d.RunDraft(ctx, in, capUSD)
	if err != nil {
		return fmt.Errorf("email-draft: emaildraft.Run: %w", err)
	}

	// The one place cleartext meets the body. EmailDraft.body at rest
	// is a sanctioned cleartext location per 02-data-model.md.
	body := substitutePasscode(draft.Body, code)

	draftID := d.NewDraftID()
	now := d.Now().UTC().Format(time.RFC3339)
	row := EmailDraftRow{
		PK:         "BUSINESS#" + biz.ID,
		SK:         "EMAIL_DRAFT#" + draftID,
		Type:       "EmailDraft",
		ID:         draftID,
		WebsiteID:  web.ID,
		ContactID:  contactID,
		Subject:    draft.Subject,
		Body:       body,
		OptOutLine: profile.OptOutLine,
		WordCount:  draft.WordCount,
		ModelID:    bedrock.ModelHaiku45,
		PromptID:   "email.v1",
		Status:     "draft",
		CreatedAt:  now,
		UpdatedAt:  now,
		Etag:       randomHex(16),
	}
	if err := d.PutDraft(ctx, row); err != nil {
		return fmt.Errorf("email-draft: put draft: %w", err)
	}

	out := pkgevents.New("outreach.email_ready", consumerName, EmailReadyDetail{
		BusinessID: biz.ID,
		DraftID:    draftID,
		WebsiteID:  web.ID,
		ContactID:  contactID,
		WordCount:  draft.WordCount,
	}).WithCorrelation(env.CorrelationID).WithCausation(env.EventID)
	if err := d.Publish(ctx, out); err != nil {
		return fmt.Errorf("email-draft: publish outreach.email_ready: %w", err)
	}
	logger.Info("email-draft.completed",
		"draftId", draftID,
		"wordCount", draft.WordCount,
		// Deliberately omit subject + body (body carries the cleartext).
	)
	return nil
}

// substitutePasscode replaces the {{PASSCODE}} placeholder with the
// cleartext. The intrinsic post-validator guarantees exactly one
// occurrence, so a single ReplaceAll is correct and complete.
func substitutePasscode(body, code string) string {
	return strings.ReplaceAll(body, schemas.PasscodePlaceholder, code)
}

func firstName(full string) string {
	if i := strings.IndexByte(full, ' '); i >= 0 {
		return full[:i]
	}
	return full
}

func auditSummary(a *AuditRow) string {
	if a.Qualitative != nil {
		return a.Qualitative.Summary
	}
	return ""
}

// --- row shapes ---------------------------------------------------------

type BusinessRow struct {
	ID       string `dynamodbav:"id"`
	Name     string `dynamodbav:"name"`
	Domain   string `dynamodbav:"domain"`
	Vertical string `dynamodbav:"vertical"`
	Location string `dynamodbav:"location"`
}

// WebsiteRow — only the fields email-draft reads. passcodeCipher is
// KMS-decrypted; never logged.
type WebsiteRow struct {
	ID             string `dynamodbav:"id"`
	Status         string `dynamodbav:"status"`
	PreviewURL     string `dynamodbav:"previewUrl"`
	PasscodeCipher string `dynamodbav:"passcodeCipher,omitempty"`
}

type AuditRow struct {
	ID          string            `dynamodbav:"id"`
	Score       int               `dynamodbav:"score"`
	Qualitative *AuditQualitative `dynamodbav:"qualitative,omitempty"`
}

type AuditQualitative struct {
	Summary string `dynamodbav:"summary"`
}

type ContactRow struct {
	ID   string `dynamodbav:"id"`
	Name string `dynamodbav:"name"`
	Role string `dynamodbav:"role"`
}

// EmailDraftRow mirrors 02-data-model.md § "EmailDraft".
type EmailDraftRow struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	Type       string `dynamodbav:"type"`
	ID         string `dynamodbav:"id"`
	WebsiteID  string `dynamodbav:"websiteId"`
	ContactID  string `dynamodbav:"contactId,omitempty"`
	Subject    string `dynamodbav:"subject"`
	Body       string `dynamodbav:"body"`
	OptOutLine string `dynamodbav:"optOutLine"`
	WordCount  int    `dynamodbav:"wordCount"`
	ModelID    string `dynamodbav:"modelId"`
	PromptID   string `dynamodbav:"promptId"`
	Status     string `dynamodbav:"status"`
	CreatedAt  string `dynamodbav:"createdAt"`
	UpdatedAt  string `dynamodbav:"updatedAt"`
	Etag       string `dynamodbav:"etag"`
}

// --- AWS wiring (production) -------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return runDeps{}, fmt.Errorf("email-draft: AWS config: %w", err)
	}
	kmsKeyID := os.Getenv("PASSCODE_KMS_KEY_ID")
	if kmsKeyID == "" {
		return runDeps{}, errors.New("email-draft: PASSCODE_KMS_KEY_ID is not set")
	}
	kmsClient := kms.NewFromConfig(cfg)
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	return runDeps{
		GetBusiness:      getBusiness,
		GetWebsite:       getWebsite,
		GetLatestAudit:   getLatestAudit,
		GetLatestContact: getLatestContact,
		GetTone:          tone.GetOrDefault,
		Decrypt: func(ctx context.Context, ct string) (string, error) {
			return passcode.DecryptCleartext(ctx, kmsClient, ct)
		},
		RunDraft: emaildraft.Run,
		PutDraft: putDraft,
		Publish: func(ctx context.Context, env pkgevents.Envelope[EmailReadyDetail]) error {
			return pkgevents.Publish(ctx, publisher, env)
		},
		CapUSD:     killswitch.CapUSD,
		Now:        time.Now,
		NewDraftID: func() string { return randomHex(16) },
	}, nil
}

func getBusiness(ctx context.Context, businessID string) (*BusinessRow, error) {
	out, err := getItem(ctx, "BUSINESS#"+businessID, "PROFILE")
	if err != nil || out == nil {
		return nil, err
	}
	var r BusinessRow
	if err := attributevalue.UnmarshalMap(out, &r); err != nil {
		return nil, fmt.Errorf("unmarshal business %s: %w", businessID, err)
	}
	return &r, nil
}

func getWebsite(ctx context.Context, businessID, websiteID string) (*WebsiteRow, error) {
	out, err := getItem(ctx, "BUSINESS#"+businessID, "WEBSITE#"+websiteID)
	if err != nil || out == nil {
		return nil, err
	}
	var r WebsiteRow
	if err := attributevalue.UnmarshalMap(out, &r); err != nil {
		return nil, fmt.Errorf("unmarshal website %s: %w", websiteID, err)
	}
	return &r, nil
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

// latestBySKPrefix returns the newest item for a business under an
// SK prefix (lexically-highest SK; our IDs are time-sortable enough
// for "most recent" selection, and the email is best-effort on
// contact/audit anyway).
func latestBySKPrefix(ctx context.Context, businessID, prefix string) (map[string]dtypes.AttributeValue, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk":     &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			":prefix": &dtypes.AttributeValueMemberS{Value: prefix},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("query %s%s: %w", businessID, prefix, err)
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	return out.Items[0], nil
}

func getLatestAudit(ctx context.Context, businessID string) (*AuditRow, error) {
	item, err := latestBySKPrefix(ctx, businessID, "AUDIT#")
	if err != nil || item == nil {
		return nil, err
	}
	var r AuditRow
	if err := attributevalue.UnmarshalMap(item, &r); err != nil {
		return nil, fmt.Errorf("unmarshal audit: %w", err)
	}
	return &r, nil
}

func getLatestContact(ctx context.Context, businessID string) (*ContactRow, error) {
	item, err := latestBySKPrefix(ctx, businessID, "CONTACT#")
	if err != nil || item == nil {
		return nil, err
	}
	var r ContactRow
	if err := attributevalue.UnmarshalMap(item, &r); err != nil {
		return nil, fmt.Errorf("unmarshal contact: %w", err)
	}
	return &r, nil
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

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("email-draft: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
