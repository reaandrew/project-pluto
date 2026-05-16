// Package main is the reply-detector Lambda (iter 8.4). SES inbound
// receipt rule stores replies to the outreach domain in S3; an S3
// ObjectCreated notification invokes this function.
//
// Attribution is deterministic, not heuristic: the sender (iter 8.2)
// sets a plus-addressed Reply-To `outreach+<draftId>@<domain>`, so a
// reply lands at that address and the +token IS the draftId. We map
// draftId → business via the REPLYREF#<draftId> reverse index the
// sender writes at send time, then flip Business.status to
// "responded", write an EmailEvent(event=replied), and publish
// `email.replied`.
//
// NOT kill-switch gated: like ses-feedback (8.3), inbound handling is
// compliance/relationship work that must run even when outreach is
// paused. Wrapping it in WithKillSwitch(StageOutreach) would silently
// drop replies whenever sending is off.
//
// Privacy: the reply body, subject, and the prospect's address are
// NEVER logged or placed in an event — only the journey ids. Richer
// classification of the reply text is iter-8.5 reply-triage's job.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

const consumerName = "reply-detector"

// refIndex is the REPLYREF#<draftId> row written by the sender.
type refIndex struct {
	BusinessID string `dynamodbav:"businessId"`
	DraftID    string `dynamodbav:"draftId"`
	WebsiteID  string `dynamodbav:"websiteId"`
	ContactID  string `dynamodbav:"contactId"`
}

// ReplyDetail is the email.replied payload — journey ids only, never
// the reply text or the prospect's address.
type ReplyDetail struct {
	BusinessID string `json:"businessId"`
	DraftID    string `json:"draftId"`
	WebsiteID  string `json:"websiteId,omitempty"`
	ContactID  string `json:"contactId,omitempty"`
	RepliedAt  string `json:"repliedAt"`
}

type EmailEventRow struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	Type       string `dynamodbav:"type"`
	DraftID    string `dynamodbav:"draftId"`
	Event      string `dynamodbav:"event"`
	OccurredAt string `dynamodbav:"occurredAt"`
}

type runDeps struct {
	GetMail       func(ctx context.Context, bucket, key string) ([]byte, error)
	LookupRef     func(ctx context.Context, draftID string) (*refIndex, error)
	MarkResponded func(ctx context.Context, businessID, now string) error
	PutEvent      func(ctx context.Context, row EmailEventRow) error
	Publish       func(ctx context.Context, d ReplyDetail) error
	Now           func() time.Time
	ReplyDomain   string
}

func handle(ctx context.Context, evt lambdaevents.S3Event) error {
	deps, err := buildDeps(ctx)
	if err != nil {
		return err
	}
	for _, r := range evt.Records {
		if perr := processRecord(ctx, deps, r.S3.Bucket.Name, r.S3.Object.Key); perr != nil {
			// Return on first hard error so the async invoke retries;
			// the raw object also persists in the inbound bucket for
			// the 90-day lifecycle, so a reply is never silently lost.
			return perr
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
		logger.Info("reply.replay_skipped")
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
		// Malformed MIME is non-retryable — drop (the raw object stays
		// in S3 for manual inspection). No content logged.
		logger.Warn("reply.unparseable")
		return nil
	}

	draftID := extractDraftID(msg.Header, d.ReplyDomain)
	if draftID == "" {
		logger.Info("reply.unattributed", "reason", "no plus-token recipient")
		return nil
	}

	ref, err := d.LookupRef(ctx, draftID)
	if err != nil {
		return fmt.Errorf("lookup REPLYREF#%s: %w", draftID, err)
	}
	if ref == nil {
		logger.Info("reply.unattributed", "reason", "no reply-ref index", "draftId", draftID)
		return nil
	}

	now := d.Now().UTC().Format(time.RFC3339)
	if err := d.MarkResponded(ctx, ref.BusinessID, now); err != nil {
		return fmt.Errorf("mark business %s responded: %w", ref.BusinessID, err)
	}
	if err := d.PutEvent(ctx, EmailEventRow{
		PK:         "BUSINESS#" + ref.BusinessID,
		SK:         "EMAIL_EVENT#" + now + "#reply",
		Type:       "EmailEvent",
		DraftID:    ref.DraftID,
		Event:      "replied",
		OccurredAt: now,
	}); err != nil {
		return fmt.Errorf("put EmailEvent: %w", err)
	}
	if err := d.Publish(ctx, ReplyDetail{
		BusinessID: ref.BusinessID,
		DraftID:    ref.DraftID,
		WebsiteID:  ref.WebsiteID,
		ContactID:  ref.ContactID,
		RepliedAt:  now,
	}); err != nil {
		return fmt.Errorf("publish email.replied: %w", err)
	}
	logger.Info("reply.detected", "businessId", ref.BusinessID, "draftId", ref.DraftID)
	return nil
}

// extractDraftID finds the first recipient at our reply domain whose
// local part is plus-addressed and returns the +token (= draftId).
// Checks the headers a replying MUA / SES inbound populates.
func extractDraftID(h mail.Header, replyDomain string) string {
	replyDomain = strings.ToLower(strings.TrimSpace(replyDomain))
	for _, hdr := range []string{"To", "Cc", "Delivered-To", "X-Original-To", "X-Forwarded-To"} {
		for _, raw := range h[hdr] {
			addrs, err := (&mail.AddressParser{}).ParseList(raw)
			if err != nil {
				// Fall back to a tolerant single-token parse.
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
		return runDeps{}, fmt.Errorf("reply-detector: AWS config: %w", err)
	}
	replyDomain := os.Getenv("REPLY_DOMAIN")
	if replyDomain == "" {
		return runDeps{}, errors.New("reply-detector: REPLY_DOMAIN not set")
	}
	s3c := s3.NewFromConfig(cfg)
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	return runDeps{
		GetMail: func(ctx context.Context, bucket, key string) ([]byte, error) {
			out, err := s3c.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(key),
			})
			if err != nil {
				return nil, err
			}
			defer func() { _ = out.Body.Close() }()
			return io.ReadAll(out.Body)
		},
		LookupRef:     lookupRef,
		MarkResponded: markResponded,
		PutEvent:      putEvent,
		Publish: func(ctx context.Context, det ReplyDetail) error {
			return pkgevents.Publish(ctx, publisher, pkgevents.New("email.replied", consumerName, det))
		},
		Now:         time.Now,
		ReplyDomain: replyDomain,
	}, nil
}

func lookupRef(ctx context.Context, draftID string) (*refIndex, error) {
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
	if err != nil {
		return nil, fmt.Errorf("get REPLYREF#%s: %w", draftID, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var r refIndex
	if err := attributevalue.UnmarshalMap(out.Item, &r); err != nil {
		return nil, fmt.Errorf("unmarshal REPLYREF: %w", err)
	}
	return &r, nil
}

// markResponded flips Business.status + gsi1pk to "responded" and
// stamps respondedAt. Idempotent (re-applying is a no-op write) and
// guarded so a later "converted" business is never regressed.
func markResponded(ctx context.Context, businessID, now string) error {
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
		UpdateExpression: aws.String("SET #s = :s, gsi1pk = :pk, respondedAt = :ra, updatedAt = :ts"),
		ExpressionAttributeNames: map[string]string{
			"#s": "status",
		},
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":s":   &dtypes.AttributeValueMemberS{Value: "responded"},
			":pk":  &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#responded"},
			":ra":  &dtypes.AttributeValueMemberS{Value: now},
			":ts":  &dtypes.AttributeValueMemberS{Value: now},
			":con": &dtypes.AttributeValueMemberS{Value: "converted"},
		},
		ConditionExpression: aws.String("attribute_exists(pk) AND #s <> :con"),
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			// Row vanished or already "converted" — nothing to do.
			return nil
		}
		return fmt.Errorf("update business %s status: %w", businessID, err)
	}
	return nil
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

func main() {
	lambda.Start(handle)
}
