// Package main is the backlog-promoter Lambda. EventBridge routes
// `queue.slot.freed` events here via an SQS main queue (with DLQ);
// each event signals that an item left the review queue, freeing one
// slot. The promoter picks the highest-priority backlog entry
// (Business where status=qualified AND awaitingPromotion=true),
// clears the flag, and publishes `website.qualified` so the preview
// generator picks it up.
//
// queue.slot.freed itself isn't emitted yet — iter 6.x (operator
// approve/reject UI) wires the producer. This Lambda ships ready so
// the EventBridge rule is in place from day one.
//
// Per-record pipeline:
//
//  1. Decode the envelope (events.FromSQS).
//  2. idempotency.WithIdempotency("backlog-promoter", env.EventID).
//  3. Query gsi1 for BUSINESS#STATUS#qualified, Filter
//     awaitingPromotion=true, ScanIndexForward=false, Limit 1.
//  4. Read the most recent Qualification row for that business.
//  5. UpdateItem on Business: awaitingPromotion=false (the row stays
//     in the `qualified` partition; only the flag flips).
//  6. Publish `website.qualified` so the preview generator wakes up.
//
// Entry-level killswitch wraps StageAudit (the promoter rolls up to
// the audit stage — same as the qualifier — per killswitch.StageMap).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
)

const consumerName = "backlog-promoter"

// QueueSlotFreedDetail is the `detail` payload of queue.slot.freed.
// The spec lists the event name but not the schema; we include the
// businessId of the just-departed item so observability traces
// connect cleanly. The promoter doesn't actually need it for the
// promotion logic itself.
type QueueSlotFreedDetail struct {
	BusinessID     string `json:"businessId"`
	PreviousStatus string `json:"previousStatus,omitempty"`
}

// WebsiteQualifiedDetail mirrors the lambdas/qualifier emitted shape.
// Duplicated for the same reason as in lambdas/qualifier — sibling
// `package main`s can't import each other.
type WebsiteQualifiedDetail struct {
	BusinessID      string  `json:"businessId"`
	QualificationID string  `json:"qualificationId"`
	PriorityScore   float64 `json:"priorityScore"`
	AuditID         string  `json:"auditId"`
}

// QualificationRow is the subset of the Qualification item we read.
type QualificationRow struct {
	ID            string  `dynamodbav:"id"`
	Qualified     bool    `dynamodbav:"qualified"`
	PriorityScore float64 `dynamodbav:"priorityScore"`
	AuditID       string  `dynamodbav:"auditId"`
}

// BusinessRow is the subset of the Business item we read.
type BusinessRow struct {
	ID                string  `dynamodbav:"id"`
	PriorityScore     float64 `dynamodbav:"-"`
	AwaitingPromotion bool    `dynamodbav:"awaitingPromotion"`
}

// runDeps is the testable surface.
type runDeps struct {
	FindTopBacklog            func(ctx context.Context) (*BusinessRow, error)
	LatestQualificationForBiz func(ctx context.Context, businessID string) (*QualificationRow, error)
	ClearAwaitingPromotion    func(ctx context.Context, businessID string) error
	PublishQualified          func(ctx context.Context, env pkgevents.Envelope[WebsiteQualifiedDetail]) error
	Now                       func() time.Time
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	err := killswitch.WithKillSwitch(ctx, killswitch.StageAudit, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		out, err := pkgevents.Consume[QueueSlotFreedDetail](ctx, raw, func(ctx context.Context, env pkgevents.Envelope[QueueSlotFreedDetail]) error {
			return processRecord(ctx, deps, env)
		})
		resp = out
		return err
	})
	return resp, err
}

func processRecord(ctx context.Context, d runDeps, env pkgevents.Envelope[QueueSlotFreedDetail]) error {
	logger := applog.FromContext(ctx).With(
		"eventId", env.EventID,
		"freedBusinessId", env.Detail.BusinessID,
	)
	_, err := idempotency.WithIdempotency(ctx, consumerName, env.EventID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, runOne(ctx, d, env, logger)
	})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("backlog-promoter.replay.skipped")
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, env pkgevents.Envelope[QueueSlotFreedDetail], logger *slog.Logger) error {
	top, err := d.FindTopBacklog(ctx)
	if err != nil {
		return fmt.Errorf("backlog-promoter: find top: %w", err)
	}
	if top == nil {
		logger.Info("backlog-promoter.nothing_to_promote")
		return nil
	}

	qual, err := d.LatestQualificationForBiz(ctx, top.ID)
	if err != nil {
		return fmt.Errorf("backlog-promoter: latest qualification: %w", err)
	}
	if qual == nil {
		// Business row says awaitingPromotion but no Qualification row
		// exists. Anomalous — log + skip rather than DLQ; another
		// queue.slot.freed will trigger a retry against a different
		// candidate.
		logger.Warn("backlog-promoter.qualification.missing", "businessId", top.ID)
		return nil
	}

	if err := d.ClearAwaitingPromotion(ctx, top.ID); err != nil {
		return fmt.Errorf("backlog-promoter: clear flag: %w", err)
	}

	// Promotion starts a new event chain — the freeing event's
	// correlationID belongs to that OTHER business's chain. The
	// promoted business's downstream emits flow from this new event.
	out := pkgevents.New("website.qualified", consumerName, WebsiteQualifiedDetail{
		BusinessID:      top.ID,
		QualificationID: qual.ID,
		PriorityScore:   qual.PriorityScore,
		AuditID:         qual.AuditID,
	}).WithCausation(env.EventID)
	if err := d.PublishQualified(ctx, out); err != nil {
		return fmt.Errorf("backlog-promoter: publish website.qualified: %w", err)
	}
	logger.Info("backlog-promoter.promoted",
		"businessId", top.ID,
		"qualificationId", qual.ID,
		"priorityScore", qual.PriorityScore,
	)
	return nil
}

// --- AWS wiring (production) ---------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	return runDeps{
		FindTopBacklog:            findTopBacklog,
		LatestQualificationForBiz: latestQualificationForBiz,
		ClearAwaitingPromotion:    clearAwaitingPromotion,
		PublishQualified: func(ctx context.Context, env pkgevents.Envelope[WebsiteQualifiedDetail]) error {
			return pkgevents.Publish(ctx, publisher, env)
		},
		Now: time.Now,
	}, nil
}

// findTopBacklog Queries gsi1 for BUSINESS#STATUS#qualified rows where
// awaitingPromotion=true. ScanIndexForward=false gives
// highest-priority-first (gsi1sk is priority-encoded). Limit 1 —
// we only promote one slot per event.
func findTopBacklog(ctx context.Context) (*BusinessRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		IndexName:              aws.String("gsi1"),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		FilterExpression:       aws.String("awaitingPromotion = :true"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk":   &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#qualified"},
			":true": &dtypes.AttributeValueMemberBOOL{Value: true},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("query backlog: %w", err)
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	var row BusinessRow
	if err := attributevalue.UnmarshalMap(out.Items[0], &row); err != nil {
		return nil, fmt.Errorf("unmarshal backlog row: %w", err)
	}
	return &row, nil
}

// latestQualificationForBiz returns the most recent Qualification row
// for a business. Queries the main partition (pk=BUSINESS#<id>) with
// sk begins_with QUAL#, then takes the last one by createdAt. For
// iter 3.3 there's never more than one Qualification per business,
// but a future re-qualification flow could produce multiple — sort
// keeps the call total.
func latestQualificationForBiz(ctx context.Context, businessID string) (*QualificationRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk":     &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			":prefix": &dtypes.AttributeValueMemberS{Value: "QUAL#"},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("query qualifications for %s: %w", businessID, err)
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	var row QualificationRow
	if err := attributevalue.UnmarshalMap(out.Items[0], &row); err != nil {
		return nil, fmt.Errorf("unmarshal qualification: %w", err)
	}
	return &row, nil
}

// clearAwaitingPromotion flips the boolean and bumps updatedAt. The
// row stays in the same gsi1 partition (status=qualified); only
// awaitingPromotion changes.
func clearAwaitingPromotion(ctx context.Context, businessID string) error {
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
		UpdateExpression: aws.String("SET awaitingPromotion = :false, updatedAt = :ts"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":false": &dtypes.AttributeValueMemberBOOL{Value: false},
			":true":  &dtypes.AttributeValueMemberBOOL{Value: true},
			":ts":    &dtypes.AttributeValueMemberS{Value: now},
		},
		ConditionExpression: aws.String("attribute_exists(pk) AND awaitingPromotion = :true"),
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			// Someone else (manual override?) already cleared the flag.
			// Idempotent — treat as success.
			return nil
		}
		return fmt.Errorf("clear awaitingPromotion for %s: %w", businessID, err)
	}
	return nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
