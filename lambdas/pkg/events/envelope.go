// Package events implements the event envelope, EventBridge publisher, and SQS
// consumer wrapper described in .ralph/specs/03-events.md. All pipeline events
// flow through a custom EventBridge bus (pipeline${env_suffix}); consumers are
// invoked via SQS for retry + DLQ.
package events

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Source is the EventBridge event source for every event we publish.
const Source = "agency.pipeline"

// CurrentEventVersion is the wire version emitted by New. Adding a field is
// non-breaking; renaming or removing requires a new event name (".v2").
const CurrentEventVersion = 1

// Envelope is the canonical event envelope. The TypeScript mirror in
// frontend/src/api/events.ts is generated from this Go shape.
type Envelope[T any] struct {
	EventID       string    `json:"eventId"`
	EventName     string    `json:"eventName"`
	EventVersion  int       `json:"eventVersion"`
	EmittedAt     time.Time `json:"emittedAt"`
	EmittedBy     string    `json:"emittedBy"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	Detail        T         `json:"detail"`
}

// New builds an Envelope with eventID auto-generated, eventVersion = 1,
// emittedAt = now (UTC), and correlationID seeded to eventID (this event
// starts a new chain). Override the chain identifiers with WithCorrelation /
// WithCausation when emitting an event caused by another event.
func New[T any](eventName, emittedBy string, detail T) Envelope[T] {
	id := uuid.NewString()
	return Envelope[T]{
		EventID:       id,
		EventName:     eventName,
		EventVersion:  CurrentEventVersion,
		EmittedAt:     time.Now().UTC(),
		EmittedBy:     emittedBy,
		CorrelationID: id,
		Detail:        detail,
	}
}

// WithCorrelation returns a copy of the envelope with the supplied correlationID.
// Use this when continuing an existing event chain (e.g. emitting from a
// consumer that just processed an upstream event).
func (e Envelope[T]) WithCorrelation(id string) Envelope[T] {
	e.CorrelationID = id
	return e
}

// WithCausation returns a copy of the envelope with the supplied causationID
// (the eventId of the event that triggered this one).
func (e Envelope[T]) WithCausation(id string) Envelope[T] {
	e.CausationID = id
	return e
}

// Validate returns an error if any required envelope field is missing.
func (e Envelope[T]) Validate() error {
	switch {
	case e.EventID == "":
		return errors.New("events: eventId is required")
	case e.EventName == "":
		return errors.New("events: eventName is required")
	case e.EventVersion < 1:
		return errors.New("events: eventVersion must be >= 1")
	case e.EmittedAt.IsZero():
		return errors.New("events: emittedAt is required")
	case e.EmittedBy == "":
		return errors.New("events: emittedBy is required")
	case e.CorrelationID == "":
		return errors.New("events: correlationId is required")
	}
	return nil
}
