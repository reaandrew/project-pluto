package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-lambda-go/events"
)

// Unmarshal parses the raw JSON of an envelope (the EventBridge `detail`
// field, or the body of an SQS message that wraps a full EventBridge event)
// into a typed Envelope[T]. The result is validated.
func Unmarshal[T any](raw json.RawMessage) (Envelope[T], error) {
	var env Envelope[T]
	if err := json.Unmarshal(raw, &env); err != nil {
		return env, fmt.Errorf("events: unmarshalling envelope: %w", err)
	}
	if err := env.Validate(); err != nil {
		return env, err
	}
	return env, nil
}

// FromEventBridge unwraps a Lambda-runtime EventBridgeEvent into a typed
// envelope. EventBridge wraps our envelope JSON in its own message; we read
// only the `detail` field.
func FromEventBridge[T any](raw events.EventBridgeEvent) (Envelope[T], error) {
	return Unmarshal[T](raw.Detail)
}

// FromSQS unwraps an SQS message whose body is a full EventBridge event
// (the standard shape when an EventBridge rule targets an SQS queue).
func FromSQS[T any](msg events.SQSMessage) (Envelope[T], error) {
	var ebEvent events.EventBridgeEvent
	if err := json.Unmarshal([]byte(msg.Body), &ebEvent); err != nil {
		return Envelope[T]{}, fmt.Errorf("events: parsing SQS body as EventBridge event: %w", err)
	}
	return FromEventBridge[T](ebEvent)
}

// HandlerFunc is the per-record handler signature for Consume. The framework
// extracts the typed envelope; the handler runs business logic and returns an
// error to mark the record as failed (kept on the queue for retry / DLQ).
type HandlerFunc[T any] func(ctx context.Context, env Envelope[T]) error

// Consume drives an SQS-batch handler. Each record is decoded into a typed
// Envelope[T] and passed to fn. Records whose handler returns an error are
// reported via SQSEventResponse.BatchItemFailures so the batch as a whole
// succeeds while only the failing records are retried (partial-batch failure).
//
// Records that fail to decode are also returned as failures. Tune the queue's
// MaxReceiveCount + DLQ to bound replay.
func Consume[T any](
	ctx context.Context,
	raw events.SQSEvent,
	fn HandlerFunc[T],
) (events.SQSEventResponse, error) {
	var failures []events.SQSBatchItemFailure
	for _, msg := range raw.Records {
		env, err := FromSQS[T](msg)
		if err != nil {
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: msg.MessageId})
			continue
		}
		if err := fn(ctx, env); err != nil {
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: msg.MessageId})
			continue
		}
	}
	return events.SQSEventResponse{BatchItemFailures: failures}, nil
}
