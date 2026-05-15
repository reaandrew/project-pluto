package prompts

import (
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// emailSystemPrefix is the system-message prose for email.v1. Mirrors
// .ralph/specs/07-bedrock-prompts.md § "Prompt: email.v1 (Haiku)"
// verbatim, with SafetyRulesBlock substituted for the spec's
// `<safety_rules>…</safety_rules>` placeholder. The `<email_tone>`
// block is appended per-call by the email-draft Lambda (iter 7.2b) so
// it can vary per vertical without re-instantiating the Prompt[T].
//
// One deliberate addition to the spec's verbatim system text: the
// access-code line. The spec's caching paragraph requires the body to
// carry a deterministic `{{PASSCODE}}` placeholder (cache key uses the
// passcode HASH; the Lambda substitutes the cleartext post-cache), so
// the model is told to emit that exact token rather than invent a
// code. This is the mechanism 07-bedrock-prompts.md § email.v1
// caching mandates, not a content change.
const emailSystemPrefix = `You write short, plain, honest cold-outreach emails for a small UK web studio.
Maximum 200 words. No exaggeration. No fake urgency. No "as you requested" framing.
Reference one specific issue from the audit summary.
Mention the preview URL exactly once and the access code exactly once, on adjacent lines.
Write the access code as the exact literal token {{PASSCODE}} (it will be substituted
before sending) — never invent a code and never write a real-looking code.
Frame the access code naturally (e.g., "the code is {{PASSCODE}}" or "use access code
{{PASSCODE}}") — do not call it a "password" and do not imply they have an account with us.
Always include the opt-out line provided in the tone profile, verbatim.
The site is a private preview — never imply it is published or that the recipient asked for it.
` + SafetyRulesBlock

// EmailSystemPrefix exposes the raw system prefix so the per-call
// wrapper (lambdas/email-draft, iter 7.2b) can compose the final
// system message: EmailSystemPrefix + WrapBlock("email_tone", …).
const EmailSystemPrefix = emailSystemPrefix

// EmailV1 is the produceEmailDraft prompt: Haiku 4.5, forced tool_use
// of `produceEmailDraft`. Schema reflects schemas.EmailV1 (a flat
// struct — no oneOf/$defs, so no hand-written schema needed).
//
// Cache (07-bedrock-prompts.md § email.v1): the email-draft Lambda
// keys on sha256(business.id + websiteId + contact.id + tone.version)
// (the passcode cleartext is NEVER a cache-key input — the model only
// ever emits the {{PASSCODE}} token and never sees a real code; the
// Lambda substitutes the cleartext after the placeholder-keyed cache
// write). CacheTTL=7d.
//
// Cost class: ~$0.005/call at typical input (per
// .ralph/specs/07-bedrock-prompts.md § "Prompt: email.v1").
var EmailV1 = New(Prompt[schemas.EmailV1]{
	ID:           "email.v1",
	ModelID:      bedrock.ModelHaiku45,
	System:       emailSystemPrefix,
	ToolName:     "produceEmailDraft",
	MaxTokens:    600,
	Stage:        bedrock.StageEmail,
	EstimateUSD:  0.005,
	CacheTTL:     7 * 24 * time.Hour,
	PostValidate: schemas.ValidateEmailV1Structural,
})
