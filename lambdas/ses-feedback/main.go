// Package main is the ses-feedback Lambda. The SES configuration set
// (terraform/ses.tf) publishes Bounce/Complaint/Delivery/Reject/
// RenderingFailure events to the `ses_feedback` SNS topic; SNS fans
// them into this Lambda's SQS main queue (with DLQ).
//
// Per record (one SNS notification):
//
//  1. Parse the SNS envelope → the SES event JSON.
//  2. idempotency.WithIdempotency keyed on (sesMessageId, eventType)
//     so a redelivered notification is a no-op.
//  3. Permanent Bounce / any Complaint → add the recipient to the SES
//     account suppression list (sesv2:PutSuppressedDestination) +
//     write the project Suppression item (pk=SUPPRESSION#<lc-email>,
//     30d TTL) + publish email.bounced / email.complained.
//     Transient bounces do NOT suppress (AWS best practice).
//  4. Attribute an EmailEvent(event=bounced|complained|delivered) to
//     the business via the SESMSG#<id> reverse index the sender (iter
//     8.2) writes. If the index is missing the suppression still
//     happens (compliance is email-keyed); only the EmailEvent is
//     skipped.
//
// This handler is deliberately NOT gated on the outreach kill switch.
// Bounce/complaint processing is inbound compliance work that must run
// regardless of whether sending is paused — in fact especially when
// paused, since outreach is typically paused *because* reputation is
// degrading. Wrapping it in WithKillSwitch(StageOutreach) would, when
// the stage is off, return success and silently ack/discard the whole
// SQS batch, losing the project-level Suppression item + audit trail.
// SES already auto-suppresses at the config-set level (suppressed_
// reasons=[BOUNCE,COMPLAINT]); the explicit PutSuppressedDestination
// here is idempotent and makes the project records authoritative.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

const consumerName = "ses-feedback"

// SuppressionTTL — project Suppression items expire after 30d (per
// 02-data-model.md / 06-discovery-and-compliance.md); SES's own
// account suppression is separate and longer-lived.
const suppressionTTL = 30 * 24 * time.Hour

// snsEnvelope is the SNS-to-SQS message shape. The SES event JSON is a
// string inside Message.
type snsEnvelope struct {
	Type    string `json:"Type"`
	Message string `json:"Message"`
}

// sesNotification is the SES configuration-set event-publishing shape
// (eventType) with a fallback to the legacy notificationType.
type sesNotification struct {
	EventType        string `json:"eventType"`
	NotificationType string `json:"notificationType"`
	Mail             struct {
		MessageID string `json:"messageId"`
	} `json:"mail"`
	Bounce *struct {
		BounceType        string `json:"bounceType"`
		BounceSubType     string `json:"bounceSubType"`
		BouncedRecipients []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"bouncedRecipients"`
	} `json:"bounce,omitempty"`
	Complaint *struct {
		ComplainedRecipients []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"complainedRecipients"`
		ComplaintFeedbackType string `json:"complaintFeedbackType"`
	} `json:"complaint,omitempty"`
	Delivery *struct {
		Recipients []string `json:"recipients"`
	} `json:"delivery,omitempty"`
}

func (n sesNotification) kind() string {
	if n.EventType != "" {
		return n.EventType
	}
	return n.NotificationType
}

// FeedbackDetail is the email.bounced / email.complained / email.delivered
// payload. Carries no body — only ids + the recipient + reason.
type FeedbackDetail struct {
	BusinessID   string `json:"businessId,omitempty"`
	DraftID      string `json:"draftId,omitempty"`
	SESMessageID string `json:"sesMessageId"`
	EmailAddress string `json:"emailAddress"`
	Reason       string `json:"reason,omitempty"`
}

type msgIndex struct {
	BusinessID string `dynamodbav:"businessId"`
	DraftID    string `dynamodbav:"draftId"`
}

type SuppressionRow struct {
	PK        string `dynamodbav:"pk"`
	SK        string `dynamodbav:"sk"`
	Type      string `dynamodbav:"type"`
	Reason    string `dynamodbav:"reason"`
	AddedAt   string `dynamodbav:"addedAt"`
	ExpiresAt int64  `dynamodbav:"expires_at"`
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

type runDeps struct {
	Suppress       func(ctx context.Context, email, reason string) error
	PutSuppression func(ctx context.Context, row SuppressionRow) error
	LookupMsg      func(ctx context.Context, sesMessageID string) (*msgIndex, error)
	PutEvent       func(ctx context.Context, row EmailEventRow) error
	Publish        func(ctx context.Context, name string, d FeedbackDetail) error
	Now            func() time.Time
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	deps, err := buildDeps(ctx)
	if err != nil {
		return resp, err
	}
	for _, msg := range raw.Records {
		if perr := processRecord(ctx, deps, msg); perr != nil {
			resp.BatchItemFailures = append(resp.BatchItemFailures,
				lambdaevents.SQSBatchItemFailure{ItemIdentifier: msg.MessageId})
		}
	}
	return resp, nil
}

func processRecord(ctx context.Context, d runDeps, msg lambdaevents.SQSMessage) error {
	logger := applog.FromContext(ctx)
	var env snsEnvelope
	if err := json.Unmarshal([]byte(msg.Body), &env); err != nil {
		// Malformed SNS envelope is non-retryable — log + drop (return
		// nil so it doesn't DLQ-loop forever on poison input).
		logger.Error("ses-feedback.sns.unparseable", "err", err.Error())
		return nil
	}
	var note sesNotification
	if err := json.Unmarshal([]byte(env.Message), &note); err != nil {
		logger.Error("ses-feedback.ses.unparseable", "err", err.Error())
		return nil
	}
	kind := note.kind()
	if note.Mail.MessageID == "" {
		logger.Warn("ses-feedback.no_message_id", "kind", kind)
		return nil
	}

	_, err := idempotency.WithIdempotency(ctx, consumerName, note.Mail.MessageID+":"+kind,
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, runOne(ctx, d, note, logger)
		})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("ses-feedback.replay.skipped", "kind", kind)
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, note sesNotification, logger *slog.Logger) error {
	msgID := note.Mail.MessageID
	switch note.kind() {
	case "Bounce":
		if note.Bounce == nil {
			return nil
		}
		permanent := strings.EqualFold(note.Bounce.BounceType, "Permanent")
		for _, r := range note.Bounce.BouncedRecipients {
			if err := d.handleAdverse(ctx, msgID, r.EmailAddress, "bounced", "bounce",
				sesv2types.SuppressionListReasonBounce, permanent, logger); err != nil {
				return err
			}
		}
	case "Complaint":
		if note.Complaint == nil {
			return nil
		}
		for _, r := range note.Complaint.ComplainedRecipients {
			if err := d.handleAdverse(ctx, msgID, r.EmailAddress, "complained", "complaint",
				sesv2types.SuppressionListReasonComplaint, true, logger); err != nil {
				return err
			}
		}
	case "Delivery":
		if note.Delivery == nil {
			return nil
		}
		// Delivery never suppresses; attribute an EmailEvent if we can.
		if biz, draft := d.attribute(ctx, msgID, logger); biz != "" {
			if err := d.PutEvent(ctx, eventRow(biz, draft, "delivered", msgID, d.Now)); err != nil {
				return err
			}
			if err := d.Publish(ctx, "email.delivered", FeedbackDetail{
				BusinessID: biz, DraftID: draft, SESMessageID: msgID,
			}); err != nil {
				return err
			}
		}
	default:
		// Reject / RenderingFailure / unknown — log, no-op (not a
		// recipient-suppression event).
		logger.Info("ses-feedback.ignored", "kind", note.kind())
	}
	return nil
}

// handleAdverse is the shared bounce/complaint path. `suppress` is the
// policy decision (permanent bounce / any complaint); transient
// bounces still record an EmailEvent but are NOT suppressed.
func (d runDeps) handleAdverse(ctx context.Context, msgID, email, eventName, reason string,
	sesReason sesv2types.SuppressionListReason, suppress bool, logger *slog.Logger) error {
	if email == "" {
		return nil
	}
	if suppress {
		if err := d.Suppress(ctx, email, string(sesReason)); err != nil {
			return fmt.Errorf("ses-feedback: suppress: %w", err)
		}
		now := d.Now().UTC()
		if err := d.PutSuppression(ctx, SuppressionRow{
			PK:        "SUPPRESSION#" + strings.ToLower(email),
			SK:        "RECORD",
			Type:      "Suppression",
			Reason:    reason,
			AddedAt:   now.Format(time.RFC3339),
			ExpiresAt: now.Add(suppressionTTL).Unix(),
		}); err != nil {
			return fmt.Errorf("ses-feedback: put suppression: %w", err)
		}
	} else {
		logger.Info("ses-feedback.transient_bounce.not_suppressed") // never log the address
	}

	biz, draft := d.attribute(ctx, msgID, logger)
	if biz != "" {
		if err := d.PutEvent(ctx, eventRow(biz, draft, eventName, msgID, d.Now)); err != nil {
			return fmt.Errorf("ses-feedback: put EmailEvent: %w", err)
		}
	}
	return d.Publish(ctx, "email."+eventName, FeedbackDetail{
		BusinessID:   biz,
		DraftID:      draft,
		SESMessageID: msgID,
		EmailAddress: email,
		Reason:       reason,
	})
}

// attribute resolves the SESMSG# reverse index (written by the iter-8.2
// sender). Missing index ⇒ ("","") — suppression still happened; only
// the business-keyed EmailEvent is skipped.
func (d runDeps) attribute(ctx context.Context, msgID string, logger *slog.Logger) (businessID, draftID string) {
	idx, err := d.LookupMsg(ctx, msgID)
	if err != nil {
		logger.Warn("ses-feedback.msg_index.lookup_failed", "err", err.Error())
		return "", ""
	}
	if idx == nil {
		logger.Warn("ses-feedback.msg_index.missing") // pre-index send; suppression already done
		return "", ""
	}
	return idx.BusinessID, idx.DraftID
}

func eventRow(businessID, draftID, event, msgID string, now func() time.Time) EmailEventRow {
	ts := now().UTC().Format(time.RFC3339)
	return EmailEventRow{
		PK:           "BUSINESS#" + businessID,
		SK:           "EMAIL_EVENT#" + ts + "#" + msgID,
		Type:         "EmailEvent",
		DraftID:      draftID,
		Event:        event,
		SESMessageID: msgID,
		OccurredAt:   ts,
	}
}

// --- AWS wiring (production) -------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return runDeps{}, fmt.Errorf("ses-feedback: AWS config: %w", err)
	}
	ses := sesv2.NewFromConfig(cfg)
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	return runDeps{
		Suppress: func(ctx context.Context, email, reason string) error {
			_, err := ses.PutSuppressedDestination(ctx, &sesv2.PutSuppressedDestinationInput{
				EmailAddress: aws.String(email),
				Reason:       sesv2types.SuppressionListReason(reason),
			})
			if err != nil {
				return fmt.Errorf("PutSuppressedDestination: %w", err)
			}
			return nil
		},
		PutSuppression: putItemFn[SuppressionRow](),
		LookupMsg:      lookupMsg,
		PutEvent:       putItemRowFn,
		Publish: func(ctx context.Context, name string, det FeedbackDetail) error {
			return pkgevents.Publish(ctx, publisher, pkgevents.New(name, consumerName, det))
		},
		Now: time.Now,
	}, nil
}

func lookupMsg(ctx context.Context, sesMessageID string) (*msgIndex, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "SESMSG#" + sesMessageID},
			"sk": &dtypes.AttributeValueMemberS{Value: "RECORD"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get SESMSG#%s: %w", sesMessageID, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var m msgIndex
	if err := attributevalue.UnmarshalMap(out.Item, &m); err != nil {
		return nil, fmt.Errorf("unmarshal SESMsgIndex: %w", err)
	}
	return &m, nil
}

func putItemRowFn(ctx context.Context, row EmailEventRow) error {
	return putAny(ctx, row, "EmailEvent")
}

func putItemFn[T any]() func(context.Context, T) error {
	return func(ctx context.Context, row T) error { return putAny(ctx, row, "item") }
}

func putAny[T any](ctx context.Context, row T, what string) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", what, err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put %s: %w", what, err)
	}
	return nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
