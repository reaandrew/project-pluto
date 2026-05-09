package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

// EventBridgeAPI is the subset of the EventBridge SDK that Publisher needs.
// Defined as an interface so tests can supply a fake.
type EventBridgeAPI interface {
	PutEvents(ctx context.Context, in *eventbridge.PutEventsInput, opts ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

// Publisher emits envelopes to the project's custom EventBridge bus.
type Publisher struct {
	client  EventBridgeAPI
	busName string
}

// NewPublisher builds a Publisher backed by the AWS EventBridge SDK using the
// default credential chain. The bus name is read from EVENT_BUS_NAME (set by
// Terraform on every consumer Lambda).
func NewPublisher(ctx context.Context) (*Publisher, error) {
	busName := os.Getenv("EVENT_BUS_NAME")
	if busName == "" {
		return nil, errors.New("events: EVENT_BUS_NAME is not set")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("events: loading AWS config: %w", err)
	}
	return NewPublisherWithClient(eventbridge.NewFromConfig(cfg), busName), nil
}

// NewPublisherWithClient builds a Publisher around an injected client. Used by
// tests and by callers that want to share an EventBridge client.
func NewPublisherWithClient(client EventBridgeAPI, busName string) *Publisher {
	return &Publisher{client: client, busName: busName}
}

// BusName returns the configured EventBridge bus name (exposed for tests
// and structured logs).
func (p *Publisher) BusName() string { return p.busName }

// Publish marshals an envelope and sends it as a single PutEvents entry.
//
// It is a top-level generic function rather than a method because Go does not
// permit type parameters on methods of non-generic types. Call as
//
//	events.Publish(ctx, p, envelope)
//
// PartialFailure (a successful PutEvents call where the entry was rejected by
// EventBridge) is reported as a non-nil error.
func Publish[T any](ctx context.Context, p *Publisher, env Envelope[T]) error {
	if p == nil {
		return errors.New("events: nil publisher")
	}
	if err := env.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("events: marshalling envelope: %w", err)
	}
	out, err := p.client.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String(p.busName),
			Source:       aws.String(Source),
			DetailType:   aws.String(env.EventName),
			Detail:       aws.String(string(body)),
			Time:         aws.Time(env.EmittedAt),
		}},
	})
	if err != nil {
		return fmt.Errorf("events: PutEvents: %w", err)
	}
	if out.FailedEntryCount > 0 && len(out.Entries) > 0 {
		entry := out.Entries[0]
		code := ""
		msg := ""
		if entry.ErrorCode != nil {
			code = *entry.ErrorCode
		}
		if entry.ErrorMessage != nil {
			msg = *entry.ErrorMessage
		}
		return fmt.Errorf("events: PutEvents entry rejected: %s: %s", code, msg)
	}
	return nil
}
