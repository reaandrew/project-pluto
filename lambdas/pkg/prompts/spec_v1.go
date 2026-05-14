package prompts

import (
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// specSystemPrefix is the system-message prose. Mirrors
// .ralph/specs/07-bedrock-prompts.md § "Prompt: spec.v1 (Sonnet)" verbatim,
// with SafetyRulesBlock substituted for the placeholder. The
// <style_guide> block is appended per-call by lambdas/pkg/spec.Run so
// it can vary per vertical without re-instantiating the Prompt[T].
const specSystemPrefix = `You are a UK small-business website designer producing a single-page redesign spec.
You must work strictly from the provided business data. Do not invent facts.
Write for the operator's chosen vertical and tone (provided in the style guide).
Output is consumed by a renderer that fills fixed components — do not invent components.
` + SafetyRulesBlock

// SpecSystemPrefix exposes the raw system prefix so the per-call
// wrapper can compose the final system message
// (SpecSystemPrefix + WrapBlock("style_guide", …)).
const SpecSystemPrefix = specSystemPrefix

// SpecV1 is the produceSpec prompt: Sonnet 4.6, forced tool_use of
// `produceSpec`. Schema is the hand-written SpecV1SchemaRaw (the
// `oneOf` discriminator pattern doesn't reflect cleanly via invopop).
// CacheTTL=90d per spec; the cache key includes the style-guide
// version so a bumped guide invalidates downstream spec caches.
//
// Cost class: ~$0.075/call at typical input (per
// .ralph/specs/07-bedrock-prompts.md § "Prompt: spec.v1").
var SpecV1 = New(Prompt[schemas.SpecV1]{
	ID:           "spec.v1",
	ModelID:      bedrock.ModelSonnet46,
	System:       specSystemPrefix,
	ToolName:     "produceSpec",
	MaxTokens:    3000,
	Stage:        bedrock.StageSpec,
	EstimateUSD:  0.075,
	CacheTTL:     90 * 24 * time.Hour,
	Schema:       schemas.SpecV1SchemaRaw,
	PostValidate: schemas.ValidateSpecV1Structural,
})
