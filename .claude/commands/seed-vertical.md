---
description: Generate seed values for a new vertical — a TargetingProfile draft, a VerticalStyleGuide draft, and an EmailToneProfile draft. Pass the vertical name as $ARGUMENTS.
---

Generate seed values for a new vertical: **$ARGUMENTS**.

Read first:
- `.ralph/specs/02-data-model.md` for the three model shapes (TargetingProfile, VerticalStyleGuide, EmailToneProfile).
- `.ralph/specs/04-feedback-loops.md` for the role each plays.
- `.ralph/specs/10-quality-rules.md` for the non-negotiables (especially: no invented business facts, no "industry-leading" / "world-class" exaggerations).

Produce three JSON drafts the operator can review before persisting via the `/settings/targeting`, `/settings/style/<vertical>`, and `/settings/email-tone/<vertical>` UIs:

## TargetingProfile
- Sensible default `vertical` (the $ARGUMENTS value, lowercased, hyphenated).
- Three reasonable starter `includeKeywords` for that vertical (e.g., for "accountants": `["chartered", "tax", "self-assessment"]`).
- Three reasonable starter `excludeKeywords` (avoid franchises, lead-gen aggregators, etc.).
- `weights` defaulted to `{websiteAge:0.2, auditScore:0.3, businessSize:0.2, contactConfidence:0.2, verticalFit:0.1}`.
- `enabled: false` — operator turns on after review.
- `location`: empty — operator fills in.

## VerticalStyleGuide
- A 1-line tone description appropriate to the vertical (professional/trustworthy/plain-English/etc.).
- 3–5 `doPhrases` that ring true for that vertical's customer.
- 3–5 `dontPhrases` covering the project-wide exaggeration ban (`industry-leading`, `world-class`, `best-in-class`, `leverage`, `synergy`) plus 1–2 vertical-specific ones.
- 2–3 `antiPatterns` (visual/copy patterns to avoid for this vertical, e.g., generic stock-photo handshakes, AI-rendered staff portraits).
- Sensible `palette` (high-contrast, WCAG AA against a neutral light).
- `version: 1`.

## EmailToneProfile
- 2–3 `subjectPatterns` using `{{businessName}}` and avoiding hype.
- 2–3 `openerPatterns` using `{{firstName}}`.
- `prohibitedPhrases` covering the project-wide exaggeration ban + vertical-specific ones + always include "password" (we use "access code") + always include "industry-leading", "world-class", "leverage", "synergy", "as you requested".
- A sensible `signature` (placeholder; operator fills in real name + company + reply address).
- `optOutLine: "Reply 'no thanks' and I won't follow up."`.
- `version: 1`.

Output the three drafts as one JSON object with keys `targetingProfile`, `verticalStyleGuide`, `emailToneProfile`. The operator copy-pastes from the response into the three UI editors, OR (if they prefer) the calling Claude can offer to write directly via the BFF API once the operator approves.

**Hard rules**: Do NOT invent any business-fact-shaped content (no testimonials, no awards, no specific customer numbers, no specific prices). The seed is stylistic only.
