package schemas

import (
	"fmt"
	"strings"
)

// ReplyTriageV1 is the tool-use payload for the `replyTriage.v1`
// Bedrock prompt (Haiku 4.5) — iter 8.5.1.
//
// Spec note: .ralph/specs/07-bedrock-prompts.md does not (yet)
// enumerate replyTriage.v1; the `.ralph/` tree is read-only for the
// implementation. This struct + the prompt in
// prompts/reply_triage_v1.go are therefore the canonical definition,
// driven by .ralph/fix_plan.md items 8.5.1/8.5.2. Keep this in
// lockstep with the prompt's tool schema.
type ReplyTriageV1 struct {
	// Category is the classification of the prospect's reply.
	Category string `json:"category" jsonschema:"required,enum=unsubscribe,enum=positive_interest,enum=unknown"`
	// Confidence is the model's certainty in [0,1]. Routing thresholds
	// (fix_plan 8.5.2): unsubscribe needs >=0.8 to auto-suppress;
	// positive_interest needs >=0.6 to auto-advance; anything else
	// (incl. category=unknown or low confidence) goes to the operator
	// inbox at /replies.
	Confidence float64 `json:"confidence" jsonschema:"required,minimum=0,maximum=1"`
	// Rationale is a one-line, PII-free justification for the label
	// (shown to the operator on /replies; never logged).
	Rationale string `json:"rationale" jsonschema:"required,maxLength=200"`
}

// Valid reply-triage categories.
const (
	ReplyCategoryUnsubscribe      = "unsubscribe"
	ReplyCategoryPositiveInterest = "positive_interest"
	ReplyCategoryUnknown          = "unknown"
)

// ValidateReplyTriageV1 enforces the rules JSON Schema can't express.
// Wired to prompts.ReplyTriageV1.PostValidate so it runs on every call.
func ValidateReplyTriageV1(r ReplyTriageV1) error {
	switch r.Category {
	case ReplyCategoryUnsubscribe, ReplyCategoryPositiveInterest, ReplyCategoryUnknown:
	default:
		return fmt.Errorf("reply-triage: invalid category %q", r.Category)
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return fmt.Errorf("reply-triage: confidence %.2f out of [0,1]", r.Confidence)
	}
	// JSON Schema `required` only rejects an absent field; an empty
	// string passes. The operator relies on the rationale on /replies,
	// so reject a blank one here.
	if strings.TrimSpace(r.Rationale) == "" {
		return fmt.Errorf("reply-triage: rationale is empty")
	}
	return nil
}
