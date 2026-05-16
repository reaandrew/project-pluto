package schemas

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// tunerBannedFactPatterns extends 10-quality-rules Rule 1
// (inventedFactPatterns) with the specific business-fact categories
// the tuner.style.v1 / tuner.email-tone.v1 system prompts forbid:
// testimonials, awards/accreditation, ratings, and prices. A tuner
// must never push these into a profile.
var tunerBannedFactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)testimonial`),
	regexp.MustCompile(`(?i)\b\d+[ -]?star\b`),
	regexp.MustCompile(`(?i)\b(award|accredited|certified|rated|reviews?)\b`),
	regexp.MustCompile(`[£$€]\s?\d`),
	regexp.MustCompile(`(?i)\b(pricing|starting (at|from)|from only)\b`),
}

// matchesInventedFact reports the first invented-fact substring in s
// (Rule-1 patterns + the tuner-specific ban list), or "".
func matchesInventedFact(s string) string {
	for _, re := range inventedFactPatterns {
		if loc := re.FindString(s); loc != "" {
			return loc
		}
	}
	for _, re := range tunerBannedFactPatterns {
		if loc := re.FindString(s); loc != "" {
			return loc
		}
	}
	return ""
}

// TunerStyleV1 is the proposeStyleDelta tool-use payload for the
// `tuner.style.v1` Bedrock prompt (Sonnet). The schema is hand-written
// (TunerStyleV1SchemaRaw) verbatim from
// .ralph/specs/07-bedrock-prompts.md § "Prompt: tuner.style.v1" —
// the nullable palette fields (`["string","null"]`) don't reflect
// cleanly, so keep the struct and the raw schema in lockstep.
type TunerStyleV1 struct {
	AddDoPhrases       []string             `json:"addDoPhrases"`
	RemoveDoPhrases    []string             `json:"removeDoPhrases"`
	AddDontPhrases     []string             `json:"addDontPhrases"`
	RemoveDontPhrases  []string             `json:"removeDontPhrases"`
	AddAntiPatterns    []string             `json:"addAntiPatterns"`
	PaletteSuggestions *StylePaletteSuggest `json:"paletteSuggestions,omitempty"`
	Rationale          string               `json:"rationale"`
}

type StylePaletteSuggest struct {
	Primary      *string `json:"primary,omitempty"`
	NeutralDark  *string `json:"neutralDark,omitempty"`
	NeutralLight *string `json:"neutralLight,omitempty"`
}

// TunerStyleV1SchemaRaw mirrors the spec's proposeStyleDelta tool
// schema verbatim.
var TunerStyleV1SchemaRaw = json.RawMessage(`{
  "type": "object",
  "required": ["addDoPhrases","removeDoPhrases","addDontPhrases","removeDontPhrases","addAntiPatterns","rationale"],
  "properties": {
    "addDoPhrases":      { "type": "array", "items": { "type": "string", "maxLength": 60 }, "maxItems": 5 },
    "removeDoPhrases":   { "type": "array", "items": { "type": "string", "maxLength": 60 }, "maxItems": 5 },
    "addDontPhrases":    { "type": "array", "items": { "type": "string", "maxLength": 60 }, "maxItems": 5 },
    "removeDontPhrases": { "type": "array", "items": { "type": "string", "maxLength": 60 }, "maxItems": 5 },
    "addAntiPatterns":   { "type": "array", "items": { "type": "string", "maxLength": 120 }, "maxItems": 5 },
    "paletteSuggestions": {
      "type": "object",
      "properties": {
        "primary":     { "type": ["string","null"], "pattern": "^(#[0-9a-fA-F]{6})?$" },
        "neutralDark":  { "type": ["string","null"] },
        "neutralLight": { "type": ["string","null"] }
      }
    },
    "rationale": { "type": "string", "maxLength": 600 }
  }
}`)

// ValidateTunerStyleV1 enforces the spec's intent that JSON Schema
// can't: a non-empty rationale, and — per the system prompt's
// "Stylistic only — do not propose business-fact additions
// (testimonials, awards, prices)" plus 10-quality-rules Rule 1 — no
// invented-fact phrasing in any additive field.
func ValidateTunerStyleV1(t TunerStyleV1) error {
	if strings.TrimSpace(t.Rationale) == "" {
		return fmt.Errorf("tuner.style: rationale is empty")
	}
	var add []string
	add = append(add, t.AddDoPhrases...)
	add = append(add, t.AddDontPhrases...)
	add = append(add, t.AddAntiPatterns...)
	for _, p := range add {
		if loc := matchesInventedFact(p); loc != "" {
			return fmt.Errorf("tuner.style: proposed phrase %q matches invented-fact pattern %q (10-quality-rules Rule 1)", p, loc)
		}
	}
	return nil
}
