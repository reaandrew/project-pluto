// Package main is the reply-triage Lambda (iter 8.5.1 + 8.5.2). It is
// a SECOND consumer of the same SES inbound bucket as reply-detector
// (iter 8.4): both receive the same s3:ObjectCreated event. The split
// is deliberate — reply-detector is fast, never reads the body, and
// just flips Business.status=responded; reply-triage reads the reply
// text, asks Bedrock Haiku (replyTriage.v1) to classify it, and routes
// the outcome.
//
// Routing (fix_plan 8.5.2):
//   - unsubscribe, confidence >= 0.8  → add the sender to the SES
//     suppression list + project Suppression item, set
//     Business.status='rejected_after_review'.
//   - positive_interest, confidence >= 0.6 → Business.status='responded'
//     (idempotent with reply-detector, which already set this).
//   - anything else (category=unknown, or confidence below the bar, or
//     the Bedrock budget cap was hit) → an operator-inbox ReplyTriage
//     item surfaced at /replies (iter 8.5.3) for manual reclassify.
//
// NOT kill-switch gated — like reply-detector/ses-feedback, inbound
// relationship + compliance work must run even when outreach is
// paused. The Bedrock spend is still bounded by the per-call cost cap
// (cost.Assert inside bedrock.InvokeStructured); a cap hit degrades
// gracefully to the operator inbox rather than dropping the reply.
//
// Privacy: the reply text is sent to Bedrock for classification and a
// short excerpt is stored on the ReplyTriage item for the operator to
// read on /replies — but it is NEVER written to a log line or an
// EventBridge payload. The quoted original (which may echo the
// {{PASSCODE}} cleartext) is stripped before the excerpt is taken.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
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
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/google/uuid"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/prompts"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

const consumerName = "reply-triage"

// confidence gates (fix_plan 8.5.2).
const (
	unsubscribeMinConf = 0.8
	interestMinConf    = 0.6
)

const (
	maxBodyChars    = 4000 // cap what we send to Bedrock
	excerptChars    = 280  // operator-inbox preview length
	suppressionTTL  = 30 * 24 * time.Hour
	statusActioned  = "auto_actioned"
	statusInbox     = "operator_inbox"
	statusRejected  = "rejected_after_review"
	statusResponded = "responded"
)

type refIndex struct {
	BusinessID string `dynamodbav:"businessId"`
	DraftID    string `dynamodbav:"draftId"`
	WebsiteID  string `dynamodbav:"websiteId"`
	ContactID  string `dynamodbav:"contactId"`
}

// TriageRow is the persisted ReplyTriage item. Attributed replies are
// stored under the business; unattributable ones under a global inbox
// pk so the operator still sees them. gsi1 carries the status so
// /replies (iter 8.5.3) can list by it via the existing index.
type TriageRow struct {
	PK          string  `dynamodbav:"pk"`
	SK          string  `dynamodbav:"sk"`
	Type        string  `dynamodbav:"type"`
	ID          string  `dynamodbav:"id"`
	BusinessID  string  `dynamodbav:"businessId,omitempty"`
	DraftID     string  `dynamodbav:"draftId,omitempty"`
	Category    string  `dynamodbav:"category"`
	Confidence  float64 `dynamodbav:"confidence"`
	Rationale   string  `dynamodbav:"rationale"`
	BodyExcerpt string  `dynamodbav:"bodyExcerpt"`
	TriageState string  `dynamodbav:"triageState"` // auto_actioned | operator_inbox
	CreatedAt   string  `dynamodbav:"createdAt"`
	UpdatedAt   string  `dynamodbav:"updatedAt"`
	GSI1PK      string  `dynamodbav:"gsi1pk"`
	GSI1SK      string  `dynamodbav:"gsi1sk"`
}

// TriagedDetail is the reply.triaged event payload — ids + label only,
// never the reply text.
type TriagedDetail struct {
	BusinessID  string  `json:"businessId,omitempty"`
	DraftID     string  `json:"draftId,omitempty"`
	TriageID    string  `json:"triageId"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
	TriageState string  `json:"triageState"`
	TriagedAt   string  `json:"triagedAt"`
}

type runDeps struct {
	GetMail        func(ctx context.Context, bucket, key string) ([]byte, error)
	LookupRef      func(ctx context.Context, draftID string) (*refIndex, error)
	Classify       func(ctx context.Context, body string) (schemas.ReplyTriageV1, error)
	Suppress       func(ctx context.Context, email, reason string) error
	PutSuppression func(ctx context.Context, email, reason, nowRFC string) error
	SetStatus      func(ctx context.Context, businessID, status, nowRFC string) error
	PutTriage      func(ctx context.Context, row TriageRow) error
	Publish        func(ctx context.Context, d TriagedDetail) error
	Now            func() time.Time
	ReplyDomain    string
}

func handle(ctx context.Context, evt lambdaevents.S3Event) error {
	deps, err := buildDeps(ctx)
	if err != nil {
		return err
	}
	for _, r := range evt.Records {
		if perr := processRecord(ctx, deps, r.S3.Bucket.Name, r.S3.Object.Key); perr != nil {
			return perr // async retry; raw object persists in S3
		}
	}
	return nil
}

func processRecord(ctx context.Context, d runDeps, bucket, key string) error {
	logger := applog.FromContext(ctx)
	_, err := idempotency.WithIdempotency(ctx, consumerName, key,
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, runOne(ctx, d, bucket, key)
		})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("reply_triage.replay_skipped")
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, bucket, key string) error {
	logger := applog.FromContext(ctx)

	raw, err := d.GetMail(ctx, bucket, key)
	if err != nil {
		return fmt.Errorf("get inbound object: %w", err)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		logger.Warn("reply_triage.unparseable")
		return nil // poison MIME — drop, object stays in S3
	}

	body := newReplyText(msg)
	if strings.TrimSpace(body) == "" {
		logger.Info("reply_triage.empty_body")
		return nil
	}
	from := senderAddress(msg.Header)
	ref, _ := d.LookupRef(ctx, extractDraftID(msg.Header, d.ReplyDomain))

	now := d.Now().UTC()
	nowRFC := now.Format(time.RFC3339)

	cls, cerr := d.Classify(ctx, body)
	if cerr != nil {
		if errors.Is(cerr, cost.ErrBudgetCapExceeded) {
			// Don't lose the reply or DLQ-loop: park it for the
			// operator with an explicit unknown label.
			logger.Warn("reply_triage.budget_cap_to_inbox")
			cls = schemas.ReplyTriageV1{Category: schemas.ReplyCategoryUnknown, Confidence: 0, Rationale: "bedrock budget cap reached — manual triage"}
		} else {
			return fmt.Errorf("classify reply: %w", cerr)
		}
	}

	state := statusInbox
	switch {
	case cls.Category == schemas.ReplyCategoryUnsubscribe && cls.Confidence >= unsubscribeMinConf:
		state = statusActioned
		if from != "" {
			if err := d.Suppress(ctx, from, "unsubscribe_request"); err != nil {
				return fmt.Errorf("suppress %s: %w", redact(from), err)
			}
			if err := d.PutSuppression(ctx, from, "unsubscribe_request", nowRFC); err != nil {
				return fmt.Errorf("put suppression: %w", err)
			}
		}
		if ref != nil {
			if err := d.SetStatus(ctx, ref.BusinessID, statusRejected, nowRFC); err != nil {
				return fmt.Errorf("set rejected_after_review: %w", err)
			}
		}
	case cls.Category == schemas.ReplyCategoryPositiveInterest && cls.Confidence >= interestMinConf:
		state = statusActioned
		if ref != nil {
			if err := d.SetStatus(ctx, ref.BusinessID, statusResponded, nowRFC); err != nil {
				return fmt.Errorf("set responded: %w", err)
			}
		}
	}

	id := uuid.NewString()
	pk, sk := "REPLYTRIAGE#INBOX", "ITEM#"+nowRFC+"#"+id
	bizID, draftID := "", ""
	if ref != nil {
		bizID, draftID = ref.BusinessID, ref.DraftID
		pk, sk = "BUSINESS#"+ref.BusinessID, "REPLY_TRIAGE#"+id
	}
	row := TriageRow{
		PK: pk, SK: sk, Type: "ReplyTriage", ID: id,
		BusinessID: bizID, DraftID: draftID,
		Category: cls.Category, Confidence: cls.Confidence, Rationale: cls.Rationale,
		BodyExcerpt: excerpt(body), TriageState: state,
		CreatedAt: nowRFC, UpdatedAt: nowRFC,
		GSI1PK: "REPLYTRIAGE#STATUS#" + state,
		GSI1SK: nowRFC + "#" + id,
	}
	if err := d.PutTriage(ctx, row); err != nil {
		return fmt.Errorf("put ReplyTriage: %w", err)
	}
	if err := d.Publish(ctx, TriagedDetail{
		BusinessID: bizID, DraftID: draftID, TriageID: id,
		Category: cls.Category, Confidence: cls.Confidence,
		TriageState: state, TriagedAt: nowRFC,
	}); err != nil {
		return fmt.Errorf("publish reply.triaged: %w", err)
	}
	logger.Info("reply_triage.done",
		"businessId", bizID, "category", cls.Category, "state", state)
	return nil
}

// newReplyText extracts the human-written portion of the reply: the
// text/plain part (or the raw body for a non-multipart message), with
// the quoted original stripped so we neither feed it to Bedrock nor
// risk excerpting an echoed {{PASSCODE}} cleartext.
func newReplyText(msg *mail.Message) string {
	raw := readTextPart(msg)
	return clip(stripQuoted(raw), maxBodyChars)
}

func readTextPart(msg *mail.Message) string {
	ct := msg.Header.Get("Content-Type")
	mt, params, err := mime.ParseMediaType(ct)
	if err == nil && strings.HasPrefix(mt, "multipart/") && params["boundary"] != "" {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		for {
			p, perr := mr.NextPart()
			if perr != nil {
				break
			}
			pmt, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			if pmt == "" || strings.HasPrefix(pmt, "text/plain") {
				b, _ := io.ReadAll(io.LimitReader(p, 1<<20))
				return string(b)
			}
		}
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(msg.Body, 1<<20))
	return string(b)
}

// stripQuoted cuts at the first reply-separator and drops ">"-quoted
// lines so only the sender's new text remains.
func stripQuoted(s string) string {
	markers := []string{"-----Original Message-----", "________________________________"}
	for _, m := range markers {
		if i := strings.Index(s, m); i >= 0 {
			s = s[:i]
		}
	}
	var b strings.Builder
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, ">") {
			continue
		}
		// "On <date>, <someone> wrote:" attribution line.
		if strings.HasPrefix(t, "On ") && strings.HasSuffix(t, "wrote:") {
			break
		}
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return b.String()
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(r[:n]))
}

func excerpt(s string) string {
	one := strings.Join(strings.Fields(s), " ")
	return clip(one, excerptChars)
}

func senderAddress(h mail.Header) string {
	if a, err := mail.ParseAddress(h.Get("From")); err == nil {
		return strings.ToLower(strings.TrimSpace(a.Address))
	}
	return ""
}

func redact(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "***"
	}
	return "***" + email[at:]
}

func extractDraftID(h mail.Header, replyDomain string) string {
	replyDomain = strings.ToLower(strings.TrimSpace(replyDomain))
	for _, hdr := range []string{"To", "Cc", "Delivered-To", "X-Original-To", "X-Forwarded-To"} {
		for _, raw := range h[hdr] {
			addrs, err := (&mail.AddressParser{}).ParseList(raw)
			if err != nil {
				if a, e := mail.ParseAddress(raw); e == nil {
					addrs = []*mail.Address{a}
				} else {
					continue
				}
			}
			for _, a := range addrs {
				if tok := plusToken(a.Address, replyDomain); tok != "" {
					return tok
				}
			}
		}
	}
	return ""
}

func plusToken(addr, replyDomain string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return ""
	}
	local := addr[:at]
	domain := strings.ToLower(addr[at+1:])
	if replyDomain != "" && domain != replyDomain {
		return ""
	}
	plus := strings.IndexByte(local, '+')
	if plus < 0 || plus+1 >= len(local) {
		return ""
	}
	return local[plus+1:]
}

// --- AWS wiring (production) -------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return runDeps{}, fmt.Errorf("reply-triage: AWS config: %w", err)
	}
	replyDomain := os.Getenv("REPLY_DOMAIN")
	if replyDomain == "" {
		return runDeps{}, errors.New("reply-triage: REPLY_DOMAIN not set")
	}
	s3c := s3.NewFromConfig(cfg)
	ses := sesv2.NewFromConfig(cfg)
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	return runDeps{
		GetMail: func(ctx context.Context, bucket, key string) ([]byte, error) {
			out, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			if err != nil {
				return nil, err
			}
			defer func() { _ = out.Body.Close() }()
			return io.ReadAll(out.Body)
		},
		LookupRef: lookupRef,
		Classify: func(ctx context.Context, body string) (schemas.ReplyTriageV1, error) {
			// Bedrock spend draws from the daily *Bedrock* budget
			// (DailyBedrockUsd), not the SES email budget. killswitch
			// StageAudit/StagePreview both map to DailyBedrockUsd; the
			// per-call ledger bucket is still bedrock.StageReplyTriage
			// (set on the Prompt) so reply-triage spend is itemised.
			capUSD, err := killswitch.CapUSD(ctx, killswitch.StageAudit)
			if err != nil {
				return schemas.ReplyTriageV1{}, err
			}
			cacheKey := bedrock.CacheKey(prompts.ReplyTriageV1.ID, prompts.HashInputs(body))
			return prompts.Invoke(ctx, prompts.ReplyTriageV1,
				[]bedrock.Message{{Role: "user", Content: body}}, capUSD, cacheKey)
		},
		Suppress: func(ctx context.Context, email, _ string) error {
			_, err := ses.PutSuppressedDestination(ctx, &sesv2.PutSuppressedDestinationInput{
				EmailAddress: aws.String(email),
				Reason:       sesv2types.SuppressionListReasonComplaint,
			})
			if err != nil {
				return fmt.Errorf("PutSuppressedDestination: %w", err)
			}
			return nil
		},
		PutSuppression: putSuppression,
		SetStatus:      setStatus,
		PutTriage:      putTriage,
		Publish: func(ctx context.Context, det TriagedDetail) error {
			return pkgevents.Publish(ctx, publisher, pkgevents.New("reply.triaged", consumerName, det))
		},
		Now:         time.Now,
		ReplyDomain: replyDomain,
	}, nil
}

func lookupRef(ctx context.Context, draftID string) (*refIndex, error) {
	if draftID == "" {
		return nil, nil
	}
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "REPLYREF#" + draftID},
			"sk": &dtypes.AttributeValueMemberS{Value: "RECORD"},
		},
	})
	if err != nil || len(out.Item) == 0 {
		return nil, err
	}
	var r refIndex
	if err := attributevalue.UnmarshalMap(out.Item, &r); err != nil {
		return nil, fmt.Errorf("unmarshal REPLYREF: %w", err)
	}
	return &r, nil
}

func putSuppression(ctx context.Context, email, reason, nowRFC string) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	now, _ := time.Parse(time.RFC3339, nowRFC)
	item, err := attributevalue.MarshalMap(map[string]any{
		"pk":         "SUPPRESSION#" + strings.ToLower(email),
		"sk":         "RECORD",
		"type":       "Suppression",
		"reason":     reason,
		"addedAt":    nowRFC,
		"expires_at": now.Add(suppressionTTL).Unix(),
	})
	if err != nil {
		return fmt.Errorf("marshal Suppression: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()), Item: item,
	}); err != nil {
		return fmt.Errorf("put Suppression: %w", err)
	}
	return nil
}

// setStatus flips Business.status + gsi1pk, guarded so a converted
// business is never regressed (mirrors reply-detector.markResponded).
func setStatus(ctx context.Context, businessID, status, nowRFC string) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	_, err = client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		},
		UpdateExpression:         aws.String("SET #s = :s, gsi1pk = :pk, updatedAt = :ts"),
		ExpressionAttributeNames: map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":s":   &dtypes.AttributeValueMemberS{Value: status},
			":pk":  &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#" + status},
			":ts":  &dtypes.AttributeValueMemberS{Value: nowRFC},
			":con": &dtypes.AttributeValueMemberS{Value: "converted"},
		},
		ConditionExpression: aws.String("attribute_exists(pk) AND #s <> :con"),
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return nil
		}
		return fmt.Errorf("update business %s status: %w", businessID, err)
	}
	return nil
}

func putTriage(ctx context.Context, row TriageRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal ReplyTriage: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()), Item: item,
	}); err != nil {
		return fmt.Errorf("put ReplyTriage: %w", err)
	}
	return nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
