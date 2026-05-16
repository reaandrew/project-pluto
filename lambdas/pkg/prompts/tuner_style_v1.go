package prompts

import (
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// tunerStyleSystemPrefix mirrors .ralph/specs/07-bedrock-prompts.md §
// "Prompt: tuner.style.v1 (Sonnet)" System block verbatim, with
// SafetyRulesBlock substituted for the placeholder.
const tunerStyleSystemPrefix = `You analyze a week of operator overrides on AI-generated website specs and rendered sites.
Propose stylistic deltas to the vertical style guide. Stylistic only — do not propose
business-fact additions (testimonials, awards, prices). Be conservative; one strong
signal is better than ten weak guesses.
` + SafetyRulesBlock

// TunerStyleV1 — proposeStyleDelta: Sonnet 4.6, forced tool_use.
// Schema is hand-written (TunerStyleV1SchemaRaw) because the nullable
// palette fields don't reflect cleanly. Not effectively cached: the
// tuner's cache key includes a hash of the week's feedback batch, so a
// re-run within the window dedupes but new feedback always re-invokes.
//
// Cost class: ~$0.05/call (Sonnet, week of diffs in, small delta out).
var TunerStyleV1 = New(Prompt[schemas.TunerStyleV1]{
	ID:           "tuner.style.v1",
	ModelID:      bedrock.ModelSonnet46,
	System:       tunerStyleSystemPrefix,
	ToolName:     "proposeStyleDelta",
	MaxTokens:    1200,
	Stage:        bedrock.StageTunerStyle,
	EstimateUSD:  0.05,
	CacheTTL:     0, // tuners are NOT cached (07-bedrock-prompts.md L378 — every run wants fresh feedback)
	Schema:       schemas.TunerStyleV1SchemaRaw,
	PostValidate: schemas.ValidateTunerStyleV1,
})
