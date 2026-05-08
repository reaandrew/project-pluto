# stdlib — Idempotency Patterns (Go)

Every event handler in this project must be idempotent on `eventId`. This document is the reusable how, in Go.

## Pattern: `lambdas/pkg/idempotency/`

```go
// lambdas/pkg/idempotency/idempotency.go
package idempotency

import (
    "context"
    "errors"
    "time"

    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
    "github.com/aws/aws-sdk-go-v2/service/dynamodb"
    "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

    "github.com/<org>/<project>/lambdas/pkg/ddb"
)

type Record struct {
    PK        string `dynamodbav:"pk"`
    SK        string `dynamodbav:"sk"`
    Type      string `dynamodbav:"type"`
    Consumer  string `dynamodbav:"consumer"`
    CreatedAt string `dynamodbav:"createdAt"`
    ExpiresAt int64  `dynamodbav:"expires_at"` // skeleton TTL attribute
}

// WithIdempotency runs fn at most once per eventID across replays of the
// same EventBridge event. If the eventID has already been processed by this
// consumer, fn is NOT called and (zero, ErrAlreadyProcessed) is returned.
func WithIdempotency[T any](
    ctx context.Context,
    consumer, eventID string,
    fn func(context.Context) (T, error),
) (T, error) {
    var zero T

    rec := Record{
        PK:        "IDEMP#" + eventID,
        SK:        "RECORD",
        Type:      "IdempotencyRecord",
        Consumer:  consumer,
        CreatedAt: time.Now().UTC().Format(time.RFC3339),
        ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
    }
    item, err := attributevalue.MarshalMap(rec)
    if err != nil {
        return zero, err
    }

    _, err = ddb.Client.PutItem(ctx, &dynamodb.PutItemInput{
        TableName:           aws.String(ddb.TableName()),
        Item:                item,
        ConditionExpression: aws.String("attribute_not_exists(pk)"),
    })

    if err != nil {
        var ccfe *types.ConditionalCheckFailedException
        if errors.As(err, &ccfe) {
            return zero, ErrAlreadyProcessed
        }
        return zero, err
    }

    return fn(ctx)
}

var ErrAlreadyProcessed = errors.New("idempotency: event already processed")
```

## Usage in a handler

```go
// lambdas/audit/main.go
package main

import (
    "context"
    "errors"

    "github.com/aws/aws-lambda-go/events"
    "github.com/aws/aws-lambda-go/lambda"

    "github.com/<org>/<project>/lambdas/pkg/idempotency"
    pkgevents "github.com/<org>/<project>/lambdas/pkg/events"
    "github.com/<org>/<project>/lambdas/pkg/killswitch"
)

const consumer = "audit"

func handle(ctx context.Context, raw events.EventBridgeEvent) error {
    if !killswitch.Allowed(ctx, "audit") {
        return nil
    }
    env, err := pkgevents.UnmarshalEnvelope[pkgevents.BusinessFoundDetail](raw)
    if err != nil {
        return err
    }

    _, err = idempotency.WithIdempotency(ctx, consumer, env.EventID, func(ctx context.Context) (struct{}, error) {
        return struct{}{}, runAudit(ctx, env.Detail)
    })
    if errors.Is(err, idempotency.ErrAlreadyProcessed) {
        return nil // success, already processed
    }
    return err
}

func main() { lambda.Start(handle) }
```

## Replay tests

Every consumer must have a `idempotency_test.go`:

```go
func TestHandlerIsIdempotent(t *testing.T) {
    ctx := context.Background()
    ev := makeEvent(t, "fixed-uuid")

    require.NoError(t, handle(ctx, ev))           // first call: does work
    require.Equal(t, 1, len(testdb.Audits()))
    require.Equal(t, 1, bedrockMock.CallCount())

    require.NoError(t, handle(ctx, ev))           // replay: no-op
    require.Equal(t, 1, len(testdb.Audits()))
    require.Equal(t, 1, bedrockMock.CallCount())
}
```

## When the work itself isn't naturally idempotent

If the work creates a row whose ID is generated server-side, derive the ID from the event:

```go
// uuid v5 from a fixed namespace + the event ID
auditID := uuid.NewSHA1(auditNamespace, []byte(env.EventID)).String()
```

That way a replayed event produces the same ID and the same row.

## SES sending

The `lambdas/sender/` SES caller passes `MessageDeduplicationId = sha256(contactID + websiteID)`. SES drops duplicates within the 5-minute dedup window, and the `EmailEvent` insert is also conditioned on `attribute_not_exists` so a replay can't double-record.

## What NOT to do

- Don't rely on EventBridge's own dedup — it isn't strong enough across replays.
- Don't put the idempotency check inside the business-logic function; it must be at the handler entry so the business function is impure-but-once.
- Don't reuse one `eventID` to mean different things across consumers; each consumer's idempotency window is its own (different `consumer` value, same `eventID` partition key — but different `pk` because the `pk` is `IDEMP#<eventID>` only). If two consumers both consume the same event, both write `IDEMP#<eventID>` rows distinguished by `sk` or by adding the consumer to the pk: `IDEMP#<consumer>#<eventID>`. **Use the second pattern** — it makes the per-consumer history independently queryable.
- Don't use sleep/random delays as a "good-enough" guard.
