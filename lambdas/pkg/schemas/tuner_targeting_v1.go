package schemas

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TunerTargetingV1 is the proposeTargetingDelta tool-use payload for
// the `tuner.targeting.v1` Bedrock prompt (Haiku). Schema hand-written
// verbatim from .ralph/specs/07-bedrock-prompts.md.
type TunerTargetingV1 struct {
	AddIncludeKeywords []string                   `json:"addIncludeKeywords"`
	AddExcludeKeywords []string                   `json:"addExcludeKeywords"`
	WeightDeltas       TunerTargetingWeightDeltas `json:"weightDeltas"`
	Rationale          string                     `json:"rationale"`
}

type TunerTargetingWeightDeltas struct {
	WebsiteAge        float64 `json:"websiteAge,omitempty"`
	AuditScore        float64 `json:"auditScore,omitempty"`
	BusinessSize      float64 `json:"businessSize,omitempty"`
	ContactConfidence float64 `json:"contactConfidence,omitempty"`
	VerticalFit       float64 `json:"verticalFit,omitempty"`
}

var TunerTargetingV1SchemaRaw = json.RawMessage(`{
  "type": "object",
  "required": ["addIncludeKeywords","addExcludeKeywords","weightDeltas","rationale"],
  "properties": {
    "addIncludeKeywords": { "type":"array","items":{"type":"string","maxLength":40},"maxItems":10 },
    "addExcludeKeywords": { "type":"array","items":{"type":"string","maxLength":40},"maxItems":10 },
    "weightDeltas": {
      "type": "object",
      "properties": {
        "websiteAge":        { "type": "number", "minimum": -0.2, "maximum": 0.2 },
        "auditScore":        { "type": "number", "minimum": -0.2, "maximum": 0.2 },
        "businessSize":      { "type": "number", "minimum": -0.2, "maximum": 0.2 },
        "contactConfidence": { "type": "number", "minimum": -0.2, "maximum": 0.2 },
        "verticalFit":       { "type": "number", "minimum": -0.2, "maximum": 0.2 }
      }
    },
    "rationale": { "type": "string", "maxLength": 400 }
  }
}`)

const weightDeltaBound = 0.2

// ValidateTunerTargetingV1 — non-empty rationale + weight deltas
// clamped to ±0.2 (the spec's schema bounds, re-enforced because the
// model can still emit out-of-range numbers).
func ValidateTunerTargetingV1(t TunerTargetingV1) error {
	if strings.TrimSpace(t.Rationale) == "" {
		return fmt.Errorf("tuner.targeting: rationale is empty")
	}
	for name, v := range map[string]float64{
		"websiteAge":        t.WeightDeltas.WebsiteAge,
		"auditScore":        t.WeightDeltas.AuditScore,
		"businessSize":      t.WeightDeltas.BusinessSize,
		"contactConfidence": t.WeightDeltas.ContactConfidence,
		"verticalFit":       t.WeightDeltas.VerticalFit,
	} {
		if v < -weightDeltaBound || v > weightDeltaBound {
			return fmt.Errorf("tuner.targeting: weightDelta %s=%.3f out of [-0.2,0.2]", name, v)
		}
	}
	return nil
}
