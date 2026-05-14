package schemas

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SpecV1 is the tool-use payload shape for `spec.v1` (Sonnet 4.6). The
// schema involves `oneOf` discriminated unions over 7 section types,
// which invopop/jsonschema can't represent cleanly via Go reflection —
// so the schema is hand-written below as `SpecV1SchemaRaw` and the Go
// type is a pragmatic flat-ish union (one Section struct with all
// fields, all omitempty, discriminated by Type).
//
// Mirrors .ralph/specs/07-bedrock-prompts.md § "Prompt: spec.v1 (Sonnet)"
// verbatim. The schema's `oneOf` + each section's `"type": { const: "<x>" }`
// discriminator gates the model's output; the renderer (iter 5) and the
// post-validator branch on Section.Type to access the right fields.
type SpecV1 struct {
	Brand       SpecBrand       `json:"brand"`
	Page        SpecPage        `json:"page"`
	SEO         SpecSEO         `json:"seo"`
	Constraints SpecConstraints `json:"constraints"`
}

type SpecBrand struct {
	Tone        string      `json:"tone"`
	Palette     SpecPalette `json:"palette"`
	Positioning string      `json:"positioning"`
}

type SpecPalette struct {
	Primary      string `json:"primary"`
	NeutralDark  string `json:"neutralDark"`
	NeutralLight string `json:"neutralLight"`
}

type SpecPage struct {
	Sections []SpecSection `json:"sections"`
}

// SpecSection is the flat-union shape. Type is the discriminator; the
// other fields are populated according to Type per the schema's $defs.
// All omitempty so the JSON round-trip stays clean across kinds.
type SpecSection struct {
	Type string `json:"type"`

	// Hero / CTA
	Headline    string   `json:"headline,omitempty"`
	Subheadline string   `json:"subheadline,omitempty"`
	PrimaryCta  *SpecCTA `json:"primaryCta,omitempty"`
	Button      *SpecCTA `json:"button,omitempty"`
	ImagePrompt string   `json:"imagePrompt,omitempty"`

	// Services / FAQ — Items is reused; Title only on Services.
	Title string        `json:"title,omitempty"`
	Items []SpecSubItem `json:"items,omitempty"`

	// About
	Paragraph string `json:"paragraph,omitempty"`

	// Trust
	Badges []SpecBadge `json:"badges,omitempty"`

	// Contact
	Address string `json:"address,omitempty"`
	Phone   string `json:"phone,omitempty"`
	Email   string `json:"email,omitempty"`
	Hours   string `json:"hours,omitempty"`
}

type SpecCTA struct {
	Label  string `json:"label"`
	Action string `json:"action"` // "call" | "email" | "form"
}

// SpecSubItem is the items[] shape on both Services and FAQ — the
// `name`/`oneLine` Services pair and the `q`/`a` FAQ pair are merged
// into one struct via omitempty. The schema enforces the right
// combination per parent kind.
type SpecSubItem struct {
	// Services items
	Name    string `json:"name,omitempty"`
	OneLine string `json:"oneLine,omitempty"`
	// FAQ items
	Q string `json:"q,omitempty"`
	A string `json:"a,omitempty"`
}

type SpecBadge struct {
	Label string `json:"label"`
}

type SpecSEO struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords,omitempty"`
}

// SpecConstraints encodes the three non-negotiables the renderer +
// post-validator enforce. The schema requires all three to be true.
type SpecConstraints struct {
	DoNotInventTestimonials bool `json:"doNotInventTestimonials"`
	DoNotInventAwards       bool `json:"doNotInventAwards"`
	DoNotInventPrices       bool `json:"doNotInventPrices"`
}

// Section type constants. Keep in lockstep with the schema's
// `"type": { "const": "<x>" }` discriminator values.
const (
	SectionHero     = "hero"
	SectionServices = "services"
	SectionAbout    = "about"
	SectionTrust    = "trust"
	SectionFAQ      = "faq"
	SectionCTA      = "cta"
	SectionContact  = "contact"
)

// AllowedSectionTypes is the closed set the schema admits. Iteration
// order is stable for the snapshot test.
var AllowedSectionTypes = []string{
	SectionHero, SectionServices, SectionAbout, SectionTrust,
	SectionFAQ, SectionCTA, SectionContact,
}

// ValidateSpecV1Structural runs the post-validation rules the renderer
// is later told it can rely on — rules JSON Schema can't express
// (e.g. "no testimonial-shaped section even if the model invents
// one"). Returns the first violation; the caller routes to DLQ.
//
// Used by the prompt's PostValidate hook. Exposed so tests + the
// renderer can share it.
func ValidateSpecV1Structural(s SpecV1) error {
	if !s.Constraints.DoNotInventTestimonials ||
		!s.Constraints.DoNotInventAwards ||
		!s.Constraints.DoNotInventPrices {
		return errors.New("spec: constraints all three flags must be true")
	}
	if len(s.Page.Sections) < 4 || len(s.Page.Sections) > 8 {
		return fmt.Errorf("spec: page.sections count %d outside [4,8]", len(s.Page.Sections))
	}
	for i, sec := range s.Page.Sections {
		if !isAllowedSectionType(sec.Type) {
			return fmt.Errorf("spec: page.sections[%d].type=%q not in allowed set", i, sec.Type)
		}
		// Reject testimonial-shaped sections regardless of how they
		// sneak in. The schema doesn't admit a "testimonial" type at
		// all, so this also catches misnamed-type smuggling.
		all := strings.ToLower(sec.Type + " " + sec.Title + " " + sec.Headline + " " + sec.Paragraph)
		if strings.Contains(all, "testimonial") || strings.Contains(all, "review from") {
			return fmt.Errorf("spec: page.sections[%d] is testimonial-shaped — banned by safety rules", i)
		}
		// "password" is banned in user-facing copy.
		for _, text := range []string{sec.Headline, sec.Subheadline, sec.Title, sec.Paragraph} {
			if strings.Contains(strings.ToLower(text), "password") {
				return fmt.Errorf("spec: page.sections[%d] contains banned word 'password'", i)
			}
		}
	}
	return nil
}

func isAllowedSectionType(t string) bool {
	for _, allowed := range AllowedSectionTypes {
		if t == allowed {
			return true
		}
	}
	return false
}

// SpecV1SchemaRaw is the hand-written JSON Schema for the `produceSpec`
// tool-use endpoint. Bedrock accepts arbitrary JSON Schema as
// `input_schema`; we hand-write because invopop/jsonschema can't
// represent the `oneOf` + `$defs` discriminator pattern cleanly.
//
// When .ralph/specs/07-bedrock-prompts.md § "Prompt: spec.v1 (Sonnet)"
// changes, change BOTH this constant AND the SpecV1 Go struct above.
// Snapshot test catches drift between the two.
var SpecV1SchemaRaw = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["brand", "page", "seo", "constraints"],
  "properties": {
    "brand": {
      "type": "object",
      "additionalProperties": false,
      "required": ["tone", "palette", "positioning"],
      "properties": {
        "tone": { "type": "string", "maxLength": 200 },
        "palette": {
          "type": "object",
          "additionalProperties": false,
          "required": ["primary", "neutralDark", "neutralLight"],
          "properties": {
            "primary": { "type": "string", "pattern": "^#[0-9a-fA-F]{6}$" },
            "neutralDark": { "type": "string" },
            "neutralLight": { "type": "string" }
          }
        },
        "positioning": { "type": "string", "maxLength": 200 }
      }
    },
    "page": {
      "type": "object",
      "additionalProperties": false,
      "required": ["sections"],
      "properties": {
        "sections": {
          "type": "array",
          "minItems": 4,
          "maxItems": 8,
          "items": {
            "oneOf": [
              { "$ref": "#/$defs/Hero" },
              { "$ref": "#/$defs/Services" },
              { "$ref": "#/$defs/About" },
              { "$ref": "#/$defs/Trust" },
              { "$ref": "#/$defs/FAQ" },
              { "$ref": "#/$defs/CTA" },
              { "$ref": "#/$defs/Contact" }
            ]
          }
        }
      }
    },
    "seo": {
      "type": "object",
      "additionalProperties": false,
      "required": ["title", "description"],
      "properties": {
        "title": { "type": "string", "maxLength": 60 },
        "description": { "type": "string", "maxLength": 160 },
        "keywords": { "type": "array", "items": { "type": "string" }, "maxItems": 8 }
      }
    },
    "constraints": {
      "type": "object",
      "additionalProperties": false,
      "required": ["doNotInventTestimonials", "doNotInventAwards", "doNotInventPrices"],
      "properties": {
        "doNotInventTestimonials": { "const": true },
        "doNotInventAwards": { "const": true },
        "doNotInventPrices": { "const": true }
      }
    }
  },
  "$defs": {
    "Hero": {
      "type": "object",
      "additionalProperties": false,
      "required": ["type", "headline", "subheadline", "primaryCta"],
      "properties": {
        "type": { "const": "hero" },
        "headline": { "type": "string", "maxLength": 80 },
        "subheadline": { "type": "string", "maxLength": 160 },
        "primaryCta": {
          "type": "object",
          "additionalProperties": false,
          "required": ["label", "action"],
          "properties": {
            "label": { "type": "string", "maxLength": 32 },
            "action": { "type": "string", "enum": ["call", "email", "form"] }
          }
        },
        "imagePrompt": { "type": "string", "description": "DESCRIPTION ONLY — renderer chooses real asset." }
      }
    },
    "Services": {
      "type": "object",
      "additionalProperties": false,
      "required": ["type", "items"],
      "properties": {
        "type": { "const": "services" },
        "title": { "type": "string", "maxLength": 60 },
        "items": {
          "type": "array", "minItems": 3, "maxItems": 6,
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["name", "oneLine"],
            "properties": {
              "name": { "type": "string", "maxLength": 40 },
              "oneLine": { "type": "string", "maxLength": 120 }
            }
          }
        }
      }
    },
    "About": {
      "type": "object",
      "additionalProperties": false,
      "required": ["type", "paragraph"],
      "properties": {
        "type": { "const": "about" },
        "paragraph": { "type": "string", "maxLength": 400 }
      }
    },
    "Trust": {
      "type": "object",
      "additionalProperties": false,
      "required": ["type", "badges"],
      "properties": {
        "type": { "const": "trust" },
        "badges": {
          "type": "array", "maxItems": 5,
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["label"],
            "properties": { "label": { "type": "string", "maxLength": 60 } }
          }
        }
      }
    },
    "FAQ": {
      "type": "object",
      "additionalProperties": false,
      "required": ["type", "items"],
      "properties": {
        "type": { "const": "faq" },
        "items": {
          "type": "array", "minItems": 3, "maxItems": 6,
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["q", "a"],
            "properties": {
              "q": { "type": "string", "maxLength": 120 },
              "a": { "type": "string", "maxLength": 400 }
            }
          }
        }
      }
    },
    "CTA": {
      "type": "object",
      "additionalProperties": false,
      "required": ["type", "headline", "button"],
      "properties": {
        "type": { "const": "cta" },
        "headline": { "type": "string", "maxLength": 80 },
        "subheadline": { "type": "string", "maxLength": 160 },
        "button": {
          "type": "object",
          "additionalProperties": false,
          "required": ["label", "action"],
          "properties": {
            "label": { "type": "string", "maxLength": 32 },
            "action": { "type": "string", "enum": ["call", "email", "form"] }
          }
        }
      }
    },
    "Contact": {
      "type": "object",
      "additionalProperties": false,
      "required": ["type"],
      "properties": {
        "type": { "const": "contact" },
        "address": { "type": "string", "maxLength": 200 },
        "phone": { "type": "string", "maxLength": 40 },
        "email": { "type": "string", "format": "email" },
        "hours": { "type": "string", "maxLength": 200 }
      }
    }
  }
}`)
