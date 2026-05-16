package schemas

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TunerEmailToneV1 is the proposeEmailToneDelta tool-use payload for
// the `tuner.email-tone.v1` Bedrock prompt (Haiku). Schema is
// hand-written verbatim from .ralph/specs/07-bedrock-prompts.md.
type TunerEmailToneV1 struct {
	AddSubjectPatterns      []string `json:"addSubjectPatterns"`
	RemoveSubjectPatterns   []string `json:"removeSubjectPatterns"`
	AddOpenerPatterns       []string `json:"addOpenerPatterns"`
	RemoveOpenerPatterns    []string `json:"removeOpenerPatterns"`
	AddProhibitedPhrases    []string `json:"addProhibitedPhrases"`
	RemoveProhibitedPhrases []string `json:"removeProhibitedPhrases"`
	Rationale               string   `json:"rationale"`
}

var TunerEmailToneV1SchemaRaw = json.RawMessage(`{
  "type": "object",
  "required": ["addSubjectPatterns","removeSubjectPatterns","addOpenerPatterns","removeOpenerPatterns","addProhibitedPhrases","removeProhibitedPhrases","rationale"],
  "properties": {
    "addSubjectPatterns":      { "type":"array","items":{"type":"string","maxLength":80},"maxItems":5 },
    "removeSubjectPatterns":   { "type":"array","items":{"type":"string","maxLength":80},"maxItems":5 },
    "addOpenerPatterns":       { "type":"array","items":{"type":"string","maxLength":120},"maxItems":5 },
    "removeOpenerPatterns":    { "type":"array","items":{"type":"string","maxLength":120},"maxItems":5 },
    "addProhibitedPhrases":    { "type":"array","items":{"type":"string","maxLength":60},"maxItems":10 },
    "removeProhibitedPhrases": { "type":"array","items":{"type":"string","maxLength":60},"maxItems":10 },
    "rationale": { "type": "string", "maxLength": 400 }
  }
}`)

// ValidateTunerEmailToneV1 — non-empty rationale + no invented-fact
// phrasing in the additive pattern lists.
func ValidateTunerEmailToneV1(t TunerEmailToneV1) error {
	if strings.TrimSpace(t.Rationale) == "" {
		return fmt.Errorf("tuner.email-tone: rationale is empty")
	}
	var add []string
	add = append(add, t.AddSubjectPatterns...)
	add = append(add, t.AddOpenerPatterns...)
	add = append(add, t.AddProhibitedPhrases...)
	for _, p := range add {
		if loc := matchesInventedFact(p); loc != "" {
			return fmt.Errorf("tuner.email-tone: proposed pattern %q matches invented-fact pattern %q", p, loc)
		}
	}
	return nil
}
