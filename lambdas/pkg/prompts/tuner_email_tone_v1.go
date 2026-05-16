package prompts

import (
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// tunerEmailToneSystemPrefix: .ralph/specs/07-bedrock-prompts.md gives
// the proposeEmailToneDelta tool for tuner.email-tone.v1 but no System
// block; this prose is authored from the prompt's Purpose ("Propose
// deltas to an EmailToneProfile") + the iter-9 conservatism rule, and
// is the canonical definition (the .ralph tree is read-only).
const tunerEmailToneSystemPrefix = `You analyze a week of operator approvals, edits, and rejections on AI-generated cold-outreach emails for one vertical.
Propose deltas to that vertical's email-tone profile: subject patterns, opener patterns, and prohibited phrases to add or remove.
Tone/structure only — never propose inventing a business fact (testimonial, award, price, fake stat). Be conservative; one strong, repeated signal beats ten weak guesses. Leave a list empty rather than padding it.
` + SafetyRulesBlock

// TunerEmailToneV1 — proposeEmailToneDelta: Haiku 4.5, forced
// tool_use. Hand-written schema (verbatim from spec). Cache keyed on
// the feedback-batch hash (effectively per-week-of-feedback).
//
// Cost class: ~$0.004/call (Haiku, short delta out).
var TunerEmailToneV1 = New(Prompt[schemas.TunerEmailToneV1]{
	ID:           "tuner.email-tone.v1",
	ModelID:      bedrock.ModelHaiku45,
	System:       tunerEmailToneSystemPrefix,
	ToolName:     "proposeEmailToneDelta",
	MaxTokens:    600,
	Stage:        bedrock.StageTunerEmailTone,
	EstimateUSD:  0.004,
	CacheTTL:     0, // tuners are NOT cached (07-bedrock-prompts.md L378 — every run wants fresh feedback)
	Schema:       schemas.TunerEmailToneV1SchemaRaw,
	PostValidate: schemas.ValidateTunerEmailToneV1,
})
