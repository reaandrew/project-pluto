// Package idempotency provides the WithIdempotency wrapper described in
// .ralph/specs/stdlib/idempotency-patterns.md. Every event consumer wraps its
// pure work with WithIdempotency at handler entry; replays of the same event
// by the same consumer become no-ops.
//
// The first call writes a conditional record into the items table keyed on
// IDEMP#<consumer>#<eventID> with sk=RECORD; the conditional check
// (attribute_not_exists(pk)) means a concurrent replay loses the race and
// receives ErrAlreadyProcessed without re-running fn. Records expire after
// DefaultTTL via the table's TTL attribute (expires_at).
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// DefaultTTL is how long an idempotency record lives before DynamoDB TTL
// sweeps it. 24h comfortably exceeds the longest realistic SQS retry window
// for this project; replays older than that are a separate bug.
const DefaultTTL = 24 * time.Hour

// RecordType is written to the `type` attribute, distinguishing idempotency
// rows from business data when scanning the table.
const RecordType = "IdempotencyRecord"

// ErrAlreadyProcessed is returned by WithIdempotency when this consumer has
// already processed the given eventID. Handlers should treat it as success.
var ErrAlreadyProcessed = errors.New("idempotency: event already processed by this consumer")

// nowFunc is overridable in tests so deterministic CreatedAt/ExpiresAt are possible.
var nowFunc = func() time.Time { return time.Now().UTC() }

// Record is the DynamoDB item shape written by WithIdempotency.
type Record struct {
	PK        string `dynamodbav:"pk"`
	SK        string `dynamodbav:"sk"`
	Type      string `dynamodbav:"type"`
	Consumer  string `dynamodbav:"consumer"`
	EventID   string `dynamodbav:"eventId"`
	CreatedAt string `dynamodbav:"createdAt"`
	ExpiresAt int64  `dynamodbav:"expires_at"`
}

// Key returns the partition key for a (consumer, eventID) pair. Each consumer
// keeps an independently queryable history of events it has processed —
// IDEMP#<consumer>#<eventID> per the spec's "second pattern" footnote.
func Key(consumer, eventID string) string {
	return fmt.Sprintf("IDEMP#%s#%s", consumer, eventID)
}

// WithIdempotency runs fn at most once per (consumer, eventID). The first call
// writes a conditional record and executes fn; subsequent calls with the same
// (consumer, eventID) return (zero, ErrAlreadyProcessed) without re-running fn.
//
// Typical handler usage:
//
//	_, err := idempotency.WithIdempotency(ctx, "audit", env.EventID, func(ctx context.Context) (struct{}, error) {
//	    return struct{}{}, runAudit(ctx, env.Detail)
//	})
//	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
//	    return nil // replay — already done
//	}
//	return err
func WithIdempotency[T any](
	ctx context.Context,
	consumer, eventID string,
	fn func(context.Context) (T, error),
) (T, error) {
	var zero T
	if consumer == "" {
		return zero, errors.New("idempotency: consumer is required")
	}
	if eventID == "" {
		return zero, errors.New("idempotency: eventID is required")
	}

	now := nowFunc()
	rec := Record{
		PK:        Key(consumer, eventID),
		SK:        "RECORD",
		Type:      RecordType,
		Consumer:  consumer,
		EventID:   eventID,
		CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(DefaultTTL).Unix(),
	}
	item, err := attributevalue.MarshalMap(rec)
	if err != nil {
		return zero, fmt.Errorf("idempotency: marshalling record: %w", err)
	}

	client, err := ddb.Client(ctx)
	if err != nil {
		return zero, fmt.Errorf("idempotency: getting DynamoDB client: %w", err)
	}
	table := ddb.TableName()
	if table == "" {
		return zero, errors.New("idempotency: ITEMS_TABLE is not set")
	}

	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var ccfe *dtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return zero, ErrAlreadyProcessed
		}
		return zero, fmt.Errorf("idempotency: writing record: %w", err)
	}

	return fn(ctx)
}
