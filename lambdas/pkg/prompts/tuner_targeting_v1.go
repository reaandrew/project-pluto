package prompts

import (
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// tunerTargetingSystemPrefix: the spec gives only the
// proposeTargetingDelta tool for tuner.targeting.v1 (no System); this
// prose is authored from the targeting feedback loop
// (04-feedback-loops.md) — repeated "not_my_audience" rejections →
// excludeKeywords; repeated qualification rejects → weight nudges —
// and is the canonical definition (.ralph is read-only).
const tunerTargetingSystemPrefix = `You analyze a week of operator decisions in the lead discovery/qualification flow for one vertical: which candidates were qualified vs rejected and the rejection reasons.
Propose deltas to that vertical's targeting profile: include/exclude keywords and small priority-weight nudges. A repeated "not my audience" signal → an excludeKeyword; a repeated "site already good" signal → a positive auditScore weight nudge.
Each weight delta must stay within [-0.2, 0.2]. Be conservative; one strong, repeated signal beats ten weak guesses. Leave a list or weight empty rather than padding it. Never propose anything that fabricates a business fact.
` + SafetyRulesBlock

// TunerTargetingV1 — proposeTargetingDelta: Haiku 4.5, forced
// tool_use. Hand-written schema (verbatim from spec). Cache keyed on
// the feedback-batch hash.
//
// Cost class: ~$0.004/call (Haiku, short delta out).
var TunerTargetingV1 = New(Prompt[schemas.TunerTargetingV1]{
	ID:           "tuner.targeting.v1",
	ModelID:      bedrock.ModelHaiku45,
	System:       tunerTargetingSystemPrefix,
	ToolName:     "proposeTargetingDelta",
	MaxTokens:    400,
	Stage:        bedrock.StageTunerTargeting,
	EstimateUSD:  0.004,
	CacheTTL:     0, // tuners are NOT cached (07-bedrock-prompts.md L378 — every run wants fresh feedback)
	Schema:       schemas.TunerTargetingV1SchemaRaw,
	PostValidate: schemas.ValidateTunerTargetingV1,
})
