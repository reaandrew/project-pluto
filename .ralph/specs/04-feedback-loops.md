# 04 — Feedback Loops

## Why this matters

The user explicitly wants three feedback channels:

1. **Lead criteria** — what kind of business gets discovered/qualified.
2. **Website design input** — what generated specs and sites should look like.
3. **Email generation** — how outreach copy reads.

Without these the system is one-shot. With them it improves measurably across cycles.

These feedback loops are **first-class product**, not a "nice-to-have late iteration". Every generator (audit, spec, site, email) ships with its capture path in the same iteration. Iteration 9 then adds the weekly tuners that turn captured feedback into reviewable profile deltas.

## Three loops, one mechanism

```
Generator  ──produces──▶  Artifact ──operator reviews──▶  Approve / Reject / Edit / Regenerate (with notes)
                                              │
                                              ▼
                              ┌─────────────────────────────────┐
                              │   feedback.captured event       │
                              │   stored as Feedback item       │
                              └────────────┬────────────────────┘
                                           ▼
                           ┌────────────────────────────────────┐
                           │  Weekly Tuner Lambda (per loop)    │
                           │  Aggregates last 7 days of feedback│
                           │  Calls Bedrock to propose a delta  │
                           │  Writes ProfileDelta item          │
                           └────────────┬───────────────────────┘
                                        ▼
                           ┌────────────────────────────────────┐
                           │  Operator reviews delta in /tuners │
                           │  Approves → applied to Profile     │
                           │  Profile.version++                  │
                           └────────────────────────────────────┘
```

The same shape applies to all three loops; only the inputs and the target Profile differ.

## Loop 1 — Lead criteria → Targeting Profile

### Capture
- The Review Queue exposes Reject buttons with a required reason picker:
  - `not_my_audience` (vertical/location off)
  - `too_small`, `too_large`
  - `existing_site_already_good`
  - `no_reachable_contact`
  - `wrong_country`
  - `manually_excluded`
  - free-text notes
- Approving a candidate also records a positive signal.

### Storage
`Feedback` items with `subject = "qualification"` (for queue rejections) and `subject = "audit"` (for "audit was wrong" overrides).

### Tuner: `targeting-tuner`
- Runs weekly via EventBridge Scheduler.
- For each enabled `TargetingProfile`, aggregates the last 7 days of rejected vs approved candidates by reason.
- Proposes weight-table adjustments and includeKeywords/excludeKeywords additions:
  - Repeated `not_my_audience` reasons in a vertical → propose adding the offending domain pattern to `excludeKeywords`.
  - Repeated `existing_site_already_good` → propose raising `weights.auditScore` and `thresholds.minTechnicalIssueScore`.
- Writes a `TargetingProfileDelta` item; emits `tuner.delta.proposed`.
- Never auto-applies. Operator confirms in `/tuners`.

### What changes downstream
On apply: `TargetingProfile.weights` and lists are updated; the `discovery` Lambda picks up the new profile on its next run (settings cached for 60s only).

## Loop 2 — Website design input → Vertical Style Guide & Spec generation

### Capture
- Spec review UI captures three actions:
  - **Approve** with no edits → positive signal for the spec template.
  - **Edit & approve** → diff between original and edited spec is the strongest training signal.
  - **Reject with notes** → explicit negative signal.
- Site preview review UI captures the same three actions on the rendered site (operator can edit the spec from here too).
- Optional: operator can pin specific phrases as "always use" or "never use" globally for the vertical.

### Storage
`Feedback` items with `subject = "spec"` and `subject = "website"`. The `originalPayload` and `editedPayload` are stored verbatim so the diff is computable.

### Tuner: `style-tuner`
- Runs weekly per vertical.
- Inputs: last 7 days of `Feedback` for `subject in {spec, website}` for that vertical, plus the current `VerticalStyleGuide`.
- Calls Sonnet 4.6 with a tool-use schema that returns:
  ```json
  {
    "addDoPhrases": [],
    "removeDoPhrases": [],
    "addDontPhrases": [],
    "removeDontPhrases": [],
    "addAntiPatterns": [],
    "paletteSuggestions": { "primary": null, "neutral": [] },
    "rationale": ""
  }
  ```
- Writes a `VerticalStyleGuideDelta` item; emits `tuner.delta.proposed`.

### What changes downstream
On apply: `VerticalStyleGuide.version++`. The `spec-generator` Lambda includes the latest style guide as system context on every run, so the next batch reflects the updates.

### Important rule
The tuner is **not allowed to add anything that would invent a business fact** (testimonials, awards, prices, certifications). The tool schema doesn't even let it. Stylistic guidance only.

## Loop 3 — Email generation → Email Tone Profile

### Capture
- Email review UI:
  - **Approve** → positive signal for the current `EmailToneProfile`.
  - **Edit & approve** → diff stored, used heavily by the tuner.
  - **Reject with notes** → strong negative signal.
- Reply detection (iteration 8) attributes positive replies back to the draft variant that was sent.

### Storage
`Feedback` items with `subject = "email"`. Replies append to the same Feedback record's `outcome` field.

### Tuner: `email-tone-tuner`
- Runs weekly per vertical.
- Inputs: last 7 days of approved drafts (with operator edits) + last 7 days of rejected drafts + reply outcomes.
- Calls Haiku 4.5 with tool-use schema:
  ```json
  {
    "addSubjectPatterns": [],
    "removeSubjectPatterns": [],
    "addOpenerPatterns": [],
    "removeOpenerPatterns": [],
    "addProhibitedPhrases": [],
    "removeProhibitedPhrases": [],
    "rationale": ""
  }
  ```
- Writes an `EmailToneProfileDelta` item.

### What changes downstream
On apply: the next batch of email drafts uses the new patterns. We track reply-rate by `EmailToneProfile.version` to see whether tuning is helping.

## Operator UI for tuner deltas

Single page `/tuners` lists pending deltas across all three loops:

```
┌─ Pending tuner suggestions ─────────────────────────────────┐
│  TargetingProfile  accountants/Manchester     proposed 2d   │
│   + exclude domains: ["jobs", "careers"]                    │
│   + weights.auditScore: 0.30 → 0.40                          │
│                                  [Apply] [Dismiss] [Diff]   │
├─────────────────────────────────────────────────────────────┤
│  VerticalStyleGuide  dentists                proposed 1d   │
│   + dontPhrases: ["industry-leading", "world-class"]        │
│                                  [Apply] [Dismiss] [Diff]   │
├─────────────────────────────────────────────────────────────┤
│  EmailToneProfile  accountants               proposed 5h   │
│   + subjectPatterns: ["Mocked up an alternate site for X"]  │
│   - prohibitedPhrases: ["leverage"]                          │
│                                  [Apply] [Dismiss] [Diff]   │
└─────────────────────────────────────────────────────────────┘
```

`Apply` mutates the live profile, bumps its `version`, writes a `Feedback` audit row recording who applied, and emits `profile.updated`. `Dismiss` records the rejection (also a useful signal — if a tuner keeps proposing the same rejected delta, it has a bug).

## What gets measured

The funnel report (iteration 11) shows reply-rate split by `EmailToneProfile.version` and approve-rate split by `VerticalStyleGuide.version`, so it's visible whether tuner-applied deltas are actually improving outcomes. If a delta hurts metrics for two consecutive cycles, an alarm flags for rollback.

## Direct overrides bypass the tuner

The operator can also edit the `TargetingProfile`, `VerticalStyleGuide`, and `EmailToneProfile` directly at any time from `/settings/targeting`, `/settings/style/<vertical>`, `/settings/email-tone/<vertical>`. The tuner never overwrites operator edits — direct edits bump `version`, after which tuner deltas are computed against the new baseline.

## Iteration mapping

- Iteration 1 — capture for targeting starts (Reject reasons in the Review Queue, even though the queue itself ships in iteration 6 — capture is a column in the Business item and reusable).
- Iteration 4 — spec-review capture ships with the spec generator.
- Iteration 5 — website-review capture ships with the generator.
- Iteration 7 — email-review capture ships with the email draft generator.
- Iteration 9 — the three weekly tuners + `/tuners` UI ship together.

The tuner runs are **explicitly opt-in for the first month**; the operator wants to see what they propose before any get auto-applied. There is no auto-apply mode in MVP.
