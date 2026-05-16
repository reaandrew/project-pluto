package prompts

import (
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// replyTriageSystemPrefix is the system message for replyTriage.v1.
//
// Spec note: .ralph/specs/07-bedrock-prompts.md does not yet enumerate
// this prompt and the `.ralph/` tree is read-only for the
// implementation, so this file + schemas.ReplyTriageV1 are the
// canonical definition per .ralph/fix_plan.md 8.5.1/8.5.2.
const replyTriageSystemPrefix = `You classify replies to a cold-outreach email from a small UK web studio that sent a private website-redesign preview.

Classify the reply into exactly one category:
- unsubscribe: the sender wants no further contact ("no thanks", "remove me", "stop", "not interested", "unsubscribe", an out-of-office that says don't contact, a hostile brush-off).
- positive_interest: the sender is interested or engaging ("looks great", "how much", "can we talk", "send more", asks a question about the preview).
- unknown: ambiguous, off-topic, auto-reply you cannot judge, or you are not sure.

Also output a confidence in [0,1] for your label and a one-line rationale.
Be conservative: if the message is short, sarcastic, or ambiguous, prefer "unknown" with low confidence rather than guessing. An auto-reply / vacation responder that does not clearly opt out is "unknown".
Judge ONLY the sender's new text; ignore any quoted original email beneath it.
Never include the sender's name, email address, phone number, or any other personal data in the rationale.
` + SafetyRulesBlock

// ReplyTriageV1 — classifyReply: Haiku 4.5, forced tool_use.
//
// Cache (this project's policy, since the spec is silent): the
// reply-triage Lambda keys on sha256 of the extracted reply text, so
// an identical reply (e.g. a duplicate SES delivery) reuses the
// classification instead of paying for Bedrock twice. CacheTTL=7d.
//
// Cost class: ~$0.004/call (Haiku, short input/output) — well under
// email.v1. cost.Assert enforces the per-call cap inside
// bedrock.InvokeStructured via the StageReplyTriage ledger bucket.
var ReplyTriageV1 = New(Prompt[schemas.ReplyTriageV1]{
	ID:           "replyTriage.v1",
	ModelID:      bedrock.ModelHaiku45,
	System:       replyTriageSystemPrefix,
	ToolName:     "classifyReply",
	MaxTokens:    300,
	Stage:        bedrock.StageReplyTriage,
	EstimateUSD:  0.004,
	CacheTTL:     7 * 24 * time.Hour,
	PostValidate: schemas.ValidateReplyTriageV1,
})
