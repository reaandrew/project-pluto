// Package main is the sender Lambda. EventBridge routes `email.approved`
// events here via an SQS main queue (with DLQ). Per record:
//
//  1. Decode the envelope; idempotency.WithIdempotency on env.EventID
//     (replay safety for the SAME approval event).
//  2. Load the EmailDraft + Contact; defensive guard
//     `EmailDraft.Status == "approved"`.
//  3. Send-once guard: a PERMANENT marker keyed on
//     sha256(contactId+websiteId) (the fix_plan's
//     "MessageDeduplicationId"). Two *different* approvals for the
//     same (contact, website) must never both send.
//  4. Suppression check (sesv2 GetSuppressedDestination) — fail
//     CLOSED: an unknown error aborts the send (compliance > delivery).
//  5. SES SendEmail (raw MIME) from the pinned outreach address with a
//     RFC 8058 one-click `List-Unsubscribe` header + the config set.
//  6. Persist the send marker + flip EmailDraft.status="sent" + write
//     an `EmailEvent` event=sent row.
//  7. Publish `email.sent` (consumed by the iter-8.5/8.6 passcode
//     cleanup/TTL sweep).
//
// Entry-level killswitch wraps StageOutreach. Live delivery is gated
// on the manual SES sandbox-out (docs/SES.md) — this Lambda is correct
// regardless; in a sandbox/non-prod env the SES call simply fails and
// the record DLQs.
//
// The unsubscribe HTTPS endpoint itself is a later sub-item; the
// header is well-formed and the mailto fallback works immediately
// (honoured by the iter-8.5 reply-triage).
package main

import (
	"context"
	"crypto/sha256"
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
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

const consumerName = "sender"

// sesPerEmailUSD is the SES send unit price (05-capacity-and-cost.md);
// the send is cost-capped against the outreach stage budget even
// though it's negligible — the project rule is "every paid call wraps
// in WithCostCap" regardless of unit cost.
const sesPerEmailUSD = 0.0001

// EmailApprovedDetail mirrors api-email's publishEmailDecision payload.
type EmailApprovedDetail struct {
	BusinessID string `json:"businessId"`
	DraftID    string `json:"draftId"`
	WebsiteID  string `json:"websiteId"`
	ContactID  string `json:"contactId"`
}

// EmailSentDetail is the `email.sent` payload. No body / no passcode.
type EmailSentDetail struct {
	BusinessID   string `json:"businessId"`
	DraftID      string `json:"draftId"`
	WebsiteID    string `json:"websiteId"`
	ContactID    string `json:"contactId"`
	SESMessageID string `json:"sesMessageId"`
	SentAt       string `json:"sentAt"`
}

type runDeps struct {
	GetDraft     func(ctx context.Context, businessID, draftID string) (*EmailDraftRow, error)
	GetContact   func(ctx context.Context, businessID, contactID string) (*ContactRow, error)
	MarkSentOnce func(ctx context.Context, businessID, dedupKey string) (bool, error) // false = already sent
	IsSuppressed func(ctx context.Context, email string) (bool, error)
	Send         func(ctx context.Context, raw []byte) (sesMessageID string, err error)
	PutEvent     func(ctx context.Context, row EmailEventRow) error
	PutMsgIndex  func(ctx context.Context, idx SESMsgIndexRow) error
	SetDraftSent func(ctx context.Context, businessID, draftID, now string) error
	Publish      func(ctx context.Context, env pkgevents.Envelope[EmailSentDetail]) error
	CapUSD       func(ctx context.Context) (float64, error)
	Now          func() time.Time
	FromAddress  string
	UnsubBase    string
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	err := killswitch.WithKillSwitch(ctx, killswitch.StageOutreach, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		out, err := pkgevents.Consume[EmailApprovedDetail](ctx, raw, func(ctx context.Context, env pkgevents.Envelope[EmailApprovedDetail]) error {
			return processRecord(ctx, deps, env)
		})
		resp = out
		return err
	})
	return resp, err
}

func processRecord(ctx context.Context, d runDeps, env pkgevents.Envelope[EmailApprovedDetail]) error {
	logger := applog.FromContext(ctx).With(
		"eventId", env.EventID,
		"businessId", env.Detail.BusinessID,
		"draftId", env.Detail.DraftID,
	)
	_, err := idempotency.WithIdempotency(ctx, consumerName, env.EventID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, runOne(ctx, d, env, logger)
	})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("sender.replay.skipped")
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, env pkgevents.Envelope[EmailApprovedDetail], logger *slog.Logger) error {
	det := env.Detail
	if det.BusinessID == "" || det.DraftID == "" || det.ContactID == "" || det.WebsiteID == "" {
		logger.Warn("sender.detail.incomplete")
		return nil
	}

	draft, err := d.GetDraft(ctx, det.BusinessID, det.DraftID)
	if err != nil {
		return fmt.Errorf("sender: get draft: %w", err)
	}
	if draft == nil {
		logger.Warn("sender.draft.missing")
		return nil
	}
	// Defensive: only send an approved draft. A race-y approve→reject
	// must not still go out.
	if draft.Status != "approved" {
		logger.Warn("sender.draft.not_approved", "status", draft.Status)
		return nil
	}

	contact, err := d.GetContact(ctx, det.BusinessID, det.ContactID)
	if err != nil {
		return fmt.Errorf("sender: get contact: %w", err)
	}
	if contact == nil || strings.TrimSpace(contact.Email) == "" {
		logger.Warn("sender.contact.no_email")
		return nil
	}

	// Suppression — fail CLOSED. (Compliance: never send to a
	// suppressed address; an unknown SES error must not be treated as
	// "not suppressed".)
	suppressed, err := d.IsSuppressed(ctx, contact.Email)
	if err != nil {
		return fmt.Errorf("sender: suppression check: %w", err)
	}
	if suppressed {
		logger.Info("sender.suppressed.skipped") // never log the address
		return nil
	}

	// Send-once guard on sha256(contactId+websiteId): two distinct
	// approval events for the same (contact, website) must not both
	// send. Marker is permanent (no TTL). Claim it BEFORE the SES call
	// so a retry after a successful send (but failed post-send write)
	// can't double-send.
	dedupKey := sendDedupKey(det.ContactID, det.WebsiteID)
	first, err := d.MarkSentOnce(ctx, det.BusinessID, dedupKey)
	if err != nil {
		return fmt.Errorf("sender: send-once marker: %w", err)
	}
	if !first {
		logger.Info("sender.already_sent.skipped", "dedupKey", dedupKey)
		return nil
	}

	capUSD, err := d.CapUSD(ctx)
	if err != nil {
		return fmt.Errorf("sender: lookup email budget: %w", err)
	}
	raw := buildRawMIME(d.FromAddress, contact.Email, draft, d.UnsubBase)
	msgID, err := cost.WithCostCap(ctx, killswitch.StageOutreach, sesPerEmailUSD, capUSD,
		func(ctx context.Context) (string, float64, error) {
			id, sendErr := d.Send(ctx, raw)
			if sendErr != nil {
				return "", 0, sendErr
			}
			return id, sesPerEmailUSD, nil
		})
	if err != nil {
		return fmt.Errorf("sender: SES send: %w", err)
	}

	now := d.Now().UTC().Format(time.RFC3339)
	if err := d.PutEvent(ctx, EmailEventRow{
		PK:           "BUSINESS#" + det.BusinessID,
		SK:           "EMAIL_EVENT#" + now + "#" + msgID,
		Type:         "EmailEvent",
		DraftID:      det.DraftID,
		Event:        "sent",
		SESMessageID: msgID,
		OccurredAt:   now,
	}); err != nil {
		return fmt.Errorf("sender: put EmailEvent: %w", err)
	}
	// Reverse index: a later SES bounce/complaint SNS notification
	// only carries the sesMessageId + recipient — never our
	// businessId. This lets the iter-8.3 ses-feedback Lambda attribute
	// the EmailEvent(bounced|complained) row to the right business.
	if err := d.PutMsgIndex(ctx, SESMsgIndexRow{
		PK:         "SESMSG#" + msgID,
		SK:         "RECORD",
		Type:       "SESMessageIndex",
		BusinessID: det.BusinessID,
		DraftID:    det.DraftID,
		WebsiteID:  det.WebsiteID,
		ContactID:  det.ContactID,
		CreatedAt:  now,
	}); err != nil {
		return fmt.Errorf("sender: put SES msg index: %w", err)
	}
	if err := d.SetDraftSent(ctx, det.BusinessID, det.DraftID, now); err != nil {
		return fmt.Errorf("sender: mark draft sent: %w", err)
	}

	out := pkgevents.New("email.sent", consumerName, EmailSentDetail{
		BusinessID:   det.BusinessID,
		DraftID:      det.DraftID,
		WebsiteID:    det.WebsiteID,
		ContactID:    det.ContactID,
		SESMessageID: msgID,
		SentAt:       now,
	}).WithCorrelation(env.CorrelationID).WithCausation(env.EventID)
	if err := d.Publish(ctx, out); err != nil {
		return fmt.Errorf("sender: publish email.sent: %w", err)
	}
	logger.Info("sender.completed", "sesMessageId", msgID) // never log subject/body/address
	return nil
}

func sendDedupKey(contactID, websiteID string) string {
	sum := sha256.Sum256([]byte(contactID + "|" + websiteID))
	return hex.EncodeToString(sum[:])
}

// buildRawMIME assembles a minimal text/plain message with the RFC 8058
// one-click List-Unsubscribe headers. The body already carries the
// free-text opt-out line (the email.v1 prompt + post-validator
// guarantee it) — the header is the machine-readable counterpart.
func buildRawMIME(from, to string, draft *EmailDraftRow, unsubBase string) []byte {
	// mailto works immediately (honoured by iter-8.5 reply-triage); the
	// https one-click endpoint is a later sub-item but the header is
	// well-formed now.
	unsubURL := strings.TrimRight(unsubBase, "/") + "/unsubscribe?d=" + draft.ID
	unsubMail := "mailto:" + from + "?subject=unsubscribe"
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + sanitiseHeader(draft.Subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("List-Unsubscribe: <" + unsubURL + ">, <" + unsubMail + ">\r\n")
	b.WriteString("List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n")
	b.WriteString("\r\n")
	b.WriteString(draft.Body)
	return []byte(b.String())
}

// sanitiseHeader strips CR/LF so a crafted subject can't inject extra
// headers (header-injection guard). Subjects are model-generated +
// operator-edited; defence-in-depth.
func sanitiseHeader(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// --- row shapes ---------------------------------------------------------

type EmailDraftRow struct {
	ID      string `dynamodbav:"id"`
	Subject string `dynamodbav:"subject"`
	Body    string `dynamodbav:"body"`
	Status  string `dynamodbav:"status"`
}

type ContactRow struct {
	ID    string `dynamodbav:"id"`
	Email string `dynamodbav:"email"`
}

type EmailEventRow struct {
	PK           string `dynamodbav:"pk"`
	SK           string `dynamodbav:"sk"`
	Type         string `dynamodbav:"type"`
	DraftID      string `dynamodbav:"draftId"`
	Event        string `dynamodbav:"event"`
	SESMessageID string `dynamodbav:"sesMessageId"`
	OccurredAt   string `dynamodbav:"occurredAt"`
}

// SESMsgIndexRow maps an SES messageId back to the business journey so
// the iter-8.3 ses-feedback consumer can attribute bounce/complaint
// notifications (which carry only the sesMessageId + recipient).
type SESMsgIndexRow struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	Type       string `dynamodbav:"type"`
	BusinessID string `dynamodbav:"businessId"`
	DraftID    string `dynamodbav:"draftId"`
	WebsiteID  string `dynamodbav:"websiteId"`
	ContactID  string `dynamodbav:"contactId"`
	CreatedAt  string `dynamodbav:"createdAt"`
}

// --- AWS wiring (production) -------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return runDeps{}, fmt.Errorf("sender: AWS config: %w", err)
	}
	from := os.Getenv("SES_FROM_ADDRESS")
	configSet := os.Getenv("SES_CONFIGURATION_SET")
	unsubBase := os.Getenv("UNSUBSCRIBE_BASE")
	if from == "" || configSet == "" || unsubBase == "" {
		return runDeps{}, errors.New("sender: SES_FROM_ADDRESS / SES_CONFIGURATION_SET / UNSUBSCRIBE_BASE not set")
	}
	ses := sesv2.NewFromConfig(cfg)
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	return runDeps{
		GetDraft:     getDraft,
		GetContact:   getContact,
		MarkSentOnce: markSentOnce,
		IsSuppressed: isSuppressedFn(ses),
		Send:         sendFn(ses, from, configSet),
		PutEvent:     putEvent,
		PutMsgIndex:  putMsgIndex,
		SetDraftSent: setDraftSent,
		Publish: func(ctx context.Context, env pkgevents.Envelope[EmailSentDetail]) error {
			return pkgevents.Publish(ctx, publisher, env)
		},
		CapUSD: func(ctx context.Context) (float64, error) {
			return killswitch.CapUSD(ctx, killswitch.StageOutreach)
		},
		Now:         time.Now,
		FromAddress: from,
		UnsubBase:   unsubBase,
	}, nil
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

func getDraft(ctx context.Context, businessID, draftID string) (*EmailDraftRow, error) {
	item, err := getItem(ctx, "BUSINESS#"+businessID, "EMAIL_DRAFT#"+draftID)
	if err != nil || item == nil {
		return nil, err
	}
	var r EmailDraftRow
	if err := attributevalue.UnmarshalMap(item, &r); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	return &r, nil
}

func getContact(ctx context.Context, businessID, contactID string) (*ContactRow, error) {
	item, err := getItem(ctx, "BUSINESS#"+businessID, "CONTACT#"+contactID)
	if err != nil || item == nil {
		return nil, err
	}
	var r ContactRow
	if err := attributevalue.UnmarshalMap(item, &r); err != nil {
		return nil, fmt.Errorf("unmarshal contact: %w", err)
	}
	return &r, nil
}

// markSentOnce writes a permanent send marker with a not-exists
// condition. Returns true if THIS call claimed it (first send), false
// if it already existed (already sent — skip).
func markSentOnce(ctx context.Context, businessID, dedupKey string) (bool, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return false, err
	}
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item: map[string]dtypes.AttributeValue{
			"pk":        &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk":        &dtypes.AttributeValueMemberS{Value: "EMAIL_SENT#" + dedupKey},
			"type":      &dtypes.AttributeValueMemberS{Value: "EmailSentMarker"},
			"createdAt": &dtypes.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
		},
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var ccf *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccf) {
			return false, nil
		}
		return false, fmt.Errorf("put send marker: %w", err)
	}
	return true, nil
}

func isSuppressedFn(ses *sesv2.Client) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, email string) (bool, error) {
		_, err := ses.GetSuppressedDestination(ctx, &sesv2.GetSuppressedDestinationInput{
			EmailAddress: aws.String(email),
		})
		if err == nil {
			return true, nil // a record exists ⇒ suppressed
		}
		var nf *sesv2types.NotFoundException
		if errors.As(err, &nf) {
			return false, nil // not on the suppression list
		}
		return false, fmt.Errorf("GetSuppressedDestination: %w", err) // fail closed
	}
}

func sendFn(ses *sesv2.Client, from, configSet string) func(context.Context, []byte) (string, error) {
	return func(ctx context.Context, raw []byte) (string, error) {
		out, err := ses.SendEmail(ctx, &sesv2.SendEmailInput{
			FromEmailAddress:     aws.String(from),
			ConfigurationSetName: aws.String(configSet),
			Content: &sesv2types.EmailContent{
				Raw: &sesv2types.RawMessage{Data: raw},
			},
		})
		if err != nil {
			return "", fmt.Errorf("SendEmail: %w", err)
		}
		return aws.ToString(out.MessageId), nil
	}
}

func putEvent(ctx context.Context, row EmailEventRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal EmailEvent: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put EmailEvent: %w", err)
	}
	return nil
}

func putMsgIndex(ctx context.Context, idx SESMsgIndexRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(idx)
	if err != nil {
		return fmt.Errorf("marshal SESMsgIndex: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put SESMsgIndex: %w", err)
	}
	return nil
}

func setDraftSent(ctx context.Context, businessID, draftID, now string) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	_, err = client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "EMAIL_DRAFT#" + draftID},
		},
		UpdateExpression: aws.String("SET #s = :sent, updatedAt = :now"),
		ExpressionAttributeNames: map[string]string{
			"#s": "status",
		},
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":sent": &dtypes.AttributeValueMemberS{Value: "sent"},
			":now":  &dtypes.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		return fmt.Errorf("update draft %s status=sent: %w", draftID, err)
	}
	return nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
