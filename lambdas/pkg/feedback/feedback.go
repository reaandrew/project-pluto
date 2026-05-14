// Package feedback writes the Feedback row + publishes feedback.captured.
// The capture path is the seed for iter 9's tuner Lambdas — every
// approve/edit/reject the operator does on a Spec/Audit/Email becomes
// one Feedback row + one event.
//
// Schema mirrors .ralph/specs/02-data-model.md § "Feedback" and
// .ralph/specs/03-events.md § feedback.captured.
package feedback

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
)

// Subject + Action constants. Closed sets the API + tuner Lambdas
// discriminate on. Keep in lockstep with the spec docs.
const (
	SubjectAudit         = "audit"
	SubjectQualification = "qualification"
	SubjectSpec          = "spec"
	SubjectWebsite       = "website"
	SubjectEmail         = "email"
)

const (
	ActionApprove    = "approve"
	ActionReject     = "reject"
	ActionEdit       = "edit"
	ActionRegenerate = "regenerate"
)

// Row mirrors the Feedback shape in 02-data-model.md.
type Row struct {
	PK              string `dynamodbav:"pk"`
	SK              string `dynamodbav:"sk"`
	Type            string `dynamodbav:"type"`
	ID              string `dynamodbav:"id"`
	Subject         string `dynamodbav:"subject"`
	SubjectID       string `dynamodbav:"subjectId"`
	BusinessID      string `dynamodbav:"businessId"`
	Actor           string `dynamodbav:"actor"`
	Action          string `dynamodbav:"action"`
	OriginalPayload string `dynamodbav:"originalPayload,omitempty"`
	EditedPayload   string `dynamodbav:"editedPayload,omitempty"`
	Notes           string `dynamodbav:"notes,omitempty"`
	Vertical        string `dynamodbav:"vertical,omitempty"`
	CreatedAt       string `dynamodbav:"createdAt"`
	GSI2PK          string `dynamodbav:"gsi2pk"`
	GSI2SK          string `dynamodbav:"gsi2sk"`
}

// CaptureInput is the per-call payload to Capture. OriginalPayload +
// EditedPayload are JSON strings — callers marshal their typed shape
// before passing in so we don't need a `json.RawMessage` round-trip on
// DDB attribute mapping.
type CaptureInput struct {
	Subject         string // audit|qualification|spec|website|email
	SubjectID       string
	BusinessID      string
	Actor           string // cognito:sub
	Action          string // approve|reject|edit|regenerate
	OriginalPayload string
	EditedPayload   string
	Notes           string
	Vertical        string
}

// Detail is the feedback.captured event payload per 03-events.md.
// The full payload bodies stay on the DDB row — the event keeps a
// "summary" hint so the tuners can decide whether to read the row.
type Detail struct {
	FeedbackID string `json:"feedbackId"`
	BusinessID string `json:"businessId"`
	Subject    string `json:"subject"`
	SubjectID  string `json:"subjectId"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	Vertical   string `json:"vertical,omitempty"`
	Summary    string `json:"summary,omitempty"`
}

// ErrInvalid signals a validation failure (missing required field,
// unknown subject/action).
var ErrInvalid = errors.New("feedback: invalid input")

// Capture writes the Feedback row and publishes feedback.captured.
// Returns the persisted row + emitted event so the caller can
// include feedbackId in the HTTP response. Errors are surfaced;
// callers route to DLQ / 5xx.
//
// The DDB write is unconditional (PutItem) — Feedback rows are
// append-only and per-operator-action; no risk of collision on
// `id`. The event publish is best-effort by spec: if publishing
// fails after persistence, the tuner reading from the DDB scan
// recovers via gsi2 even without the event.
func Capture(ctx context.Context, in CaptureInput, publisher *pkgevents.Publisher) (Row, pkgevents.Envelope[Detail], error) {
	if err := validate(in); err != nil {
		return Row{}, pkgevents.Envelope[Detail]{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	id := randomHex(16)
	now := nowFunc().UTC().Format(time.RFC3339)
	vertical := in.Vertical
	if vertical == "" {
		vertical = "default"
	}
	row := Row{
		PK:              "FEEDBACK#" + vertical,
		SK:              now + "#" + id,
		Type:            "Feedback",
		ID:              id,
		Subject:         in.Subject,
		SubjectID:       in.SubjectID,
		BusinessID:      in.BusinessID,
		Actor:           in.Actor,
		Action:          in.Action,
		OriginalPayload: in.OriginalPayload,
		EditedPayload:   in.EditedPayload,
		Notes:           in.Notes,
		Vertical:        vertical,
		CreatedAt:       now,
		GSI2PK:          "FEEDBACK#VERTICAL#" + vertical,
		GSI2SK:          in.Subject + "#" + now,
	}
	if err := putRow(ctx, row); err != nil {
		return Row{}, pkgevents.Envelope[Detail]{}, err
	}

	env := pkgevents.New("feedback.captured", "bff", Detail{
		FeedbackID: id,
		BusinessID: in.BusinessID,
		Subject:    in.Subject,
		SubjectID:  in.SubjectID,
		Actor:      in.Actor,
		Action:     in.Action,
		Vertical:   vertical,
		Summary:    in.Notes, // short hint; full payloads on the row
	})
	if publisher != nil {
		if err := pkgevents.Publish(ctx, publisher, env); err != nil {
			// Best-effort. Surface so the caller can log; tuners
			// recover via gsi2 scan even without the event.
			return row, env, fmt.Errorf("feedback: publish: %w", err)
		}
	}
	return row, env, nil
}

func validate(in CaptureInput) error {
	if in.Subject == "" {
		return errors.New("subject is required")
	}
	if !isKnownSubject(in.Subject) {
		return fmt.Errorf("subject %q not in {audit, qualification, spec, website, email}", in.Subject)
	}
	if in.SubjectID == "" {
		return errors.New("subjectId is required")
	}
	if in.BusinessID == "" {
		return errors.New("businessId is required")
	}
	if in.Actor == "" {
		return errors.New("actor (cognito:sub) is required")
	}
	if in.Action == "" {
		return errors.New("action is required")
	}
	if !isKnownAction(in.Action) {
		return fmt.Errorf("action %q not in {approve, reject, edit, regenerate}", in.Action)
	}
	return nil
}

func isKnownSubject(s string) bool {
	switch s {
	case SubjectAudit, SubjectQualification, SubjectSpec, SubjectWebsite, SubjectEmail:
		return true
	}
	return false
}

func isKnownAction(a string) bool {
	switch a {
	case ActionApprove, ActionReject, ActionEdit, ActionRegenerate:
		return true
	}
	return false
}

func putRow(ctx context.Context, row Row) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return fmt.Errorf("feedback: ddb client: %w", err)
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("feedback: marshal: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("feedback: PutItem: %w", err)
	}
	return nil
}

// nowFunc + randomHex overrides for deterministic tests.
var (
	nowFunc       = func() time.Time { return time.Now().UTC() }
	randomHexFunc = defaultRandomHex
)

// SetNowFunc overrides the time source. Tests only.
func SetNowFunc(f func() time.Time) { nowFunc = f }

// SetIDFunc overrides the id generator. Tests only.
func SetIDFunc(f func() string) {
	randomHexFunc = func(int) string { return f() }
}

func randomHex(n int) string { return randomHexFunc(n) }

func defaultRandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("feedback: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}
