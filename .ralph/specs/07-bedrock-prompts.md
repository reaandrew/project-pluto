# 07 — Bedrock Prompts

All Bedrock calls use **tool use** for structured output. Never parse free-text JSON. Every prompt is versioned (`promptId = "<name>.v<n>"`) and lives in `lambdas/pkg/prompts/<name>_v<n>.go`. The schema for each tool is a Go struct in `lambdas/pkg/schemas/`; the JSON Schema sent to Bedrock is generated from that struct via `lambdas/pkg/schemas/jsonschema.go` so the runtime payload and the Go validation can never drift.

## Cost-aware model selection

| Stage | Model | Why |
|---|---|---|
| Audit qualitative review | `anthropic.claude-haiku-4-5` | 5–10 short observations; Haiku is plenty. |
| Qualification reasoning | `anthropic.claude-haiku-4-5` | A 1-paragraph rationale + booleans; Haiku is plenty. |
| Spec generation | `anthropic.claude-sonnet-4-6` | Multi-section coherent output, headlines that need to actually read well. |
| Site copy refinement | `anthropic.claude-sonnet-4-6` | Same reasoning. |
| Email drafting | `anthropic.claude-haiku-4-5` | Short, template-bound. |
| Style-guide tuner | `anthropic.claude-sonnet-4-6` | Reads weeks of feedback diffs; needs reasoning. |
| Targeting tuner | `anthropic.claude-haiku-4-5` | Aggregates structured rejection reasons. |
| Email-tone tuner | `anthropic.claude-haiku-4-5` | Short pattern lists. |

## Common conventions

- All prompts include a `<style_guide>` block fed from `VerticalStyleGuide` for the relevant vertical.
- All prompts include a `<safety_rules>` block from `10-quality-rules.md` (excerpts referenced verbatim).
- Tool schemas are typed in `packages/contracts/schemas/` and shared between the Lambda and snapshot tests.
- Every call:
  1. Hashes (promptId + inputHash) → checks `CACHE#BEDROCK#<promptId> / <inputHash>`.
  2. On miss, invokes Bedrock with `temperature: 0.2`, `top_p: 0.9`, `max_tokens` set per prompt.
  3. Validates the tool-use response against the JSON Schema for that tool.
  4. Records spend, writes to cache, returns.

## Prompt: `audit.qualitative.v1` (Haiku)

**Purpose**: Look at a homepage HTML snapshot + the technical pre-audit and produce a qualitative review.

**Inputs**: `business {name, vertical, location}`, `technical {…}`, `htmlExcerpt` (first 8KB of body text, stripped).

**System**:
```
You are a senior conversion-design reviewer for small business websites in the UK and US.
You score sites for whether a redesign would materially increase enquiries / bookings.
Be concrete and concise. Refer to specific elements you can see in the HTML.
Never invent business facts. If you cannot see something, say so.
<safety_rules>…</safety_rules>
```

**Tool**: `recordAudit`
```json
{
  "type": "object",
  "required": ["score", "worthRedesigning", "summary", "issues"],
  "properties": {
    "score": { "type": "integer", "minimum": 0, "maximum": 100, "description": "Higher is better." },
    "worthRedesigning": { "type": "boolean" },
    "summary": { "type": "string", "maxLength": 400 },
    "issues": {
      "type": "array",
      "maxItems": 8,
      "items": {
        "type": "object",
        "required": ["type", "severity", "description"],
        "properties": {
          "type": { "enum": ["conversion","design","performance","mobile","trust","seo","accessibility"] },
          "severity": { "enum": ["low","medium","high"] },
          "description": { "type": "string", "maxLength": 200 }
        }
      }
    }
  }
}
```

`max_tokens: 800`. Cost class: ~$0.012/call at typical input.

## Prompt: `spec.v1` (Sonnet)

**Purpose**: Produce a single-page redesign spec for a qualified business.

**Inputs**: `business`, `audit.qualitative + technical`, `verticalStyleGuide`, `extractedContent` (services list, contact details, hours — pulled from the existing site by code, not the model).

**System**:
```
You are a UK small-business website designer producing a single-page redesign spec.
You must work strictly from the provided business data. Do not invent facts.
Write for the operator's chosen vertical and tone (provided in the style guide).
Output is consumed by a renderer that fills fixed components — do not invent components.
<safety_rules>…</safety_rules>
<style_guide>…</style_guide>
```

**Tool**: `produceSpec`
```json
{
  "type": "object",
  "required": ["brand", "page", "seo", "constraints"],
  "properties": {
    "brand": {
      "type": "object",
      "required": ["tone", "palette", "positioning"],
      "properties": {
        "tone": { "type": "string", "maxLength": 200 },
        "palette": {
          "type": "object",
          "required": ["primary","neutralDark","neutralLight"],
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
      "required": ["title","description"],
      "properties": {
        "title": { "type": "string", "maxLength": 60 },
        "description": { "type": "string", "maxLength": 160 },
        "keywords": { "type": "array", "items": { "type": "string" }, "maxItems": 8 }
      }
    },
    "constraints": {
      "type": "object",
      "required": ["doNotInventTestimonials","doNotInventAwards","doNotInventPrices"],
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
      "required": ["type","headline","subheadline","primaryCta"],
      "properties": {
        "type": { "const": "hero" },
        "headline": { "type": "string", "maxLength": 80 },
        "subheadline": { "type": "string", "maxLength": 160 },
        "primaryCta": { "type": "object", "required": ["label","action"],
          "properties": { "label": { "type": "string", "maxLength": 32 }, "action": { "type": "string", "enum": ["call","email","form"] } } },
        "imagePrompt": { "type": "string", "description": "DESCRIPTION ONLY — renderer chooses real asset." }
      }
    },
    "Services": {
      "type": "object",
      "required": ["type","items"],
      "properties": {
        "type": { "const": "services" },
        "title": { "type": "string", "maxLength": 60 },
        "items": {
          "type": "array", "minItems": 3, "maxItems": 6,
          "items": { "type": "object", "required": ["name","oneLine"],
            "properties": { "name": { "type": "string", "maxLength": 40 }, "oneLine": { "type": "string", "maxLength": 120 } } }
        }
      }
    },
    "About": {
      "type": "object",
      "required": ["type","paragraph"],
      "properties": {
        "type": { "const": "about" },
        "paragraph": { "type": "string", "maxLength": 400 }
      }
    },
    "Trust": {
      "type": "object",
      "required": ["type","badges"],
      "properties": {
        "type": { "const": "trust" },
        "badges": {
          "type": "array", "maxItems": 5,
          "description": "Only badges the renderer can verify exist (e.g. real industry bodies, real certifications surfaced by code from the source site). The model must NOT invent these — the renderer drops any that the source site does not display.",
          "items": { "type": "object", "required": ["label"], "properties": { "label": { "type": "string", "maxLength": 60 } } }
        }
      }
    },
    "FAQ": {
      "type": "object",
      "required": ["type","items"],
      "properties": {
        "type": { "const": "faq" },
        "items": {
          "type": "array", "minItems": 3, "maxItems": 6,
          "items": { "type": "object", "required": ["q","a"],
            "properties": { "q": { "type": "string", "maxLength": 120 }, "a": { "type": "string", "maxLength": 400 } } }
        }
      }
    },
    "CTA": {
      "type": "object",
      "required": ["type","headline","button"],
      "properties": {
        "type": { "const": "cta" },
        "headline": { "type": "string", "maxLength": 80 },
        "subheadline": { "type": "string", "maxLength": 160 },
        "button": { "type": "object", "required": ["label","action"],
          "properties": { "label": { "type": "string", "maxLength": 32 }, "action": { "type": "string", "enum": ["call","email","form"] } } }
      }
    },
    "Contact": {
      "type": "object",
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
}
```

`max_tokens: 3000`. Cost class: ~$0.075/call at typical input.

**Renderer guarantees** (enforced in code, not in the model):
- Strips any testimonial-shaped section even if the model produces one.
- Drops any Trust badge whose label is not present verbatim in the source site's text.
- Replaces `imagePrompt` with a curated stock asset (Unsplash via deterministic search) — the model never picks an actual image URL.
- Replaces palette values that fail WCAG AA contrast against `neutralLight`.

## Prompt: `email.v1` (Haiku)

**Purpose**: Draft an outreach email for an approved candidate. The email **must** include both the preview URL **and** the passcode that unlocks it.

**Inputs**: `business`, `contact {firstName,role}`, `audit.summary`, `previewUrl`, `passcode` (cleartext, KMS-decrypted by the email-draft Lambda before invoking Bedrock — passed via the prompt, never logged), `emailToneProfile`, `senderProfile`.

**System**:
```
You write short, plain, honest cold-outreach emails for a small UK web studio.
Maximum 200 words. No exaggeration. No fake urgency. No "as you requested" framing.
Reference one specific issue from the audit summary.
Mention the preview URL exactly once and the access code exactly once, on adjacent lines.
Frame the access code naturally (e.g., "the code is XXXX" or "use access code XXXX") —
do not call it a "password" and do not imply they have an account with us.
Always include the opt-out line provided in the tone profile.
The site is a private preview — never imply it is published or that the recipient asked for it.
<safety_rules>…</safety_rules>
<email_tone>…</email_tone>
```

**Tool**: `produceEmailDraft`
```json
{
  "type": "object",
  "required": ["subject","body","wordCount"],
  "properties": {
    "subject": { "type": "string", "maxLength": 80 },
    "body": { "type": "string", "maxLength": 1500 },
    "wordCount": { "type": "integer", "minimum": 60, "maximum": 200 }
  }
}
```

`max_tokens: 600`. Cost class: ~$0.005/call.

**Post-validation** (in code):
- Reject if `body` contains any prohibited phrase from `EmailToneProfile.prohibitedPhrases`.
- Reject if `body` does not contain `previewUrl` exactly once.
- Reject if `body` does not contain the literal passcode exactly once.
- Reject if `body` contains the word "password" (we use "access code" / "code" instead).
- Reject if `body` does not contain the `optOutLine`.
- Reject if `wordCount > 200`.

The passcode is the only piece of cleartext that Bedrock sees per call. The cache key includes the passcode hash (not the cleartext) so two calls with the same business + tone but different passcodes still hit the cache; the wrapper substitutes the passcode at the end via deterministic `{{PASSCODE}}` placeholder substitution before returning.

## Prompt: `tuner.style.v1` (Sonnet)

**Purpose**: Propose deltas to a `VerticalStyleGuide` based on operator feedback.

**Inputs**: `currentStyleGuide`, `feedbackBatch` (last 7 days of `subject in {spec, website}` diffs and notes for that vertical).

**System**:
```
You analyze a week of operator overrides on AI-generated website specs and rendered sites.
Propose stylistic deltas to the vertical style guide. Stylistic only — do not propose
business-fact additions (testimonials, awards, prices). Be conservative; one strong
signal is better than ten weak guesses.
```

**Tool**: `proposeStyleDelta`
```json
{
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
        "primary":   { "type": ["string","null"], "pattern": "^(#[0-9a-fA-F]{6})?$" },
        "neutralDark": { "type": ["string","null"] },
        "neutralLight":{ "type": ["string","null"] }
      }
    },
    "rationale": { "type": "string", "maxLength": 600 }
  }
}
```

## Prompt: `tuner.email-tone.v1` (Haiku)

**Purpose**: Propose deltas to an `EmailToneProfile`.

**Tool**: `proposeEmailToneDelta`
```json
{
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
}
```

## Prompt: `tuner.targeting.v1` (Haiku)

**Tool**: `proposeTargetingDelta`
```json
{
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
}
```

## Caching policy

- `audit.qualitative.v1`: cache key `(promptId, sha256(domain + html_excerpt))`. TTL 30 days. If the underlying HTML has not changed in 30 days, the audit is reused.
- `spec.v1`: cache key `(promptId, sha256(business.id + audit.id + verticalStyleGuide.version))`. TTL 90 days. Regenerated automatically when style guide bumps.
- `email.v1`: cache key `(promptId, sha256(business.id + websiteId + contact.id + tone.version))`. TTL 7 days.
- Tuner prompts: not cached (they want to see new feedback every run).

## Snapshot tests

For every prompt:
- One snapshot test per prompt-version that asserts the assembled tool-use payload (system + messages + tool schema) is byte-stable for a fixed input fixture.
- One contract test that runs the schema validator on a known-good fixture response.
- One adversarial test that confirms the post-validator rejects a fake-testimonial response, a >200-word email, etc.

These tests run on every PR; a prompt change must update the snapshot deliberately.
