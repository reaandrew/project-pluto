# 00 — Overview

## What we're building

An AI-assisted outbound website-redesign pipeline. Continuously discover small businesses with weak websites, audit and qualify them, generate a private redesign **preview** for the qualified ones, and surface the top-N candidates per day in a human review queue. The operator approves, edits, or rejects; on approval, an outreach email with the preview link is generated and sent via SES.

Operator feedback on (a) lead criteria, (b) site design, and (c) email copy flows back into targeting / spec / email generation, so the pipeline measurably improves with use.

## What we are *not* building

- Not a CRM. Lead tracking is the byproduct, not the product.
- Not autonomous outreach. The operator is the final gate for emails.
- Not a generic AI website builder. Sites are templated through a fixed component library; the AI fills slots, never freestyles HTML.
- Not a high-volume scraping engine. We use official APIs first; scraped sources are throttled, capped, robots-respecting, and treated as one provider among many.

## The shape of the product

```
                     ┌───────────────────────────┐
                     │  Targeting Profiles       │  ← operator edits niches/locations/weights
                     └────────────┬──────────────┘
                                  ▼
            ┌────────────────────────────────────────────┐
            │   Autonomous discovery → audit → qualify   │
            │   (capped daily, paused on budget breach)  │
            └────────────────────────────────────────────┘
                                  ▼
                      ┌────────────────────┐
                      │   Spec generated   │
                      │   Preview built    │
                      │   Screenshot taken │
                      └─────────┬──────────┘
                                ▼
                ┌──────────────────────────────────┐
                │   Review Queue (top-N per day)    │
                │   Approve / Edit / Regenerate /   │
                │   Reject — with notes             │
                └─────────────────┬─────────────────┘
                                  ▼
                  ┌─────────────────────────────────┐
                  │   Email drafted from approval   │
                  │   Operator approves → SES sends │
                  └────────────────┬────────────────┘
                                   ▼
                       ┌────────────────────────┐
                       │  Replies + opens +     │
                       │  conversions tracked   │
                       └───────────┬────────────┘
                                   ▼
                ┌──────────────────────────────────────┐
                │  Feedback Tuners (weekly)            │
                │  → Targeting Profiles                │
                │  → Vertical Style Guides             │
                │  → Email Tone Profiles               │
                └──────────────────────────────────────┘
```

The two key insights:

1. **The admin UI is a review queue, not a data-entry tool.** The operator never types in a business by hand.
2. **The feedback tuners close the loop.** Every operator override on a generated artifact (audit, spec, site copy, email) is a labelled training signal for the next batch.

## Operating principles

1. **Each iteration ends with a working demo.** No half-built infra living between iterations.
2. **Capacity-aware by default.** Discovery is cheap and runs broadly; preview generation and outreach are expensive and run narrowly. Operator attention is the most expensive resource — protect it with caps.
3. **Composition over freestyle.** Bedrock outputs structured JSON; code renders known-safe components.
4. **No hallucinated business facts.** No invented testimonials, awards, staff, prices, certifications, or affiliations. See `10-quality-rules.md`.
5. **Cost-aware AI.** Haiku for triage and short copy; Sonnet only for spec and headline generation. Always cache, always cap.
6. **Idempotent everywhere.** Every event handler is replay-safe. Every paid API call has a cache.
7. **Feedback is product.** If you build a generator, you build the capture path for the operator's overrides on its output, in the same iteration.

## Glossary

| Term | Meaning |
|------|---------|
| **Targeting Profile** | Operator-tuned record describing what kind of business to find (vertical, location, keywords, weights). |
| **Audit** | Combined technical (PageSpeed/HTML heuristics) + qualitative (Bedrock) assessment of an existing website. |
| **Qualification** | The decision that a business *is worth* generating a preview for. |
| **Spec** | The structured JSON that drives site generation — sections, copy, palette, SEO. |
| **Preview** | The generated static site, hosted on Cloudflare R2, served via Cloudflare Worker, indexed nowhere. |
| **Vertical Style Guide** | Tone, palette, do/don't phrases for a vertical. Mutates with feedback. |
| **Email Tone Profile** | Subject patterns, opener patterns, prohibited phrases for outreach in a vertical. Mutates with feedback. |
| **Review Queue** | The operator's primary admin surface — the top-N candidates awaiting decision today. |
| **Backlog** | Qualified businesses that haven't fit into today's review cap; promoted automatically when slots free. |

## Goals & non-goals at MVP

### Goals
- Operator can launch the pipeline with one targeting profile and an enabled kill switch.
- Pipeline runs autonomously up to caps; surfaces ≤ 10 candidates per day for review.
- Generated previews are good enough that the operator approves at least 30% of them.
- Approved candidates produce emails the operator approves at least 70% of.
- All operator overrides feed weekly tuner runs that produce reviewable diffs to targeting/style/tone profiles.
- Total monthly Bedrock + Places + SES + R2 + Pages spend at moderate caps stays under £200.

### Non-goals at MVP
- Multi-tenant.
- Mobile admin UI (responsive but not native).
- Self-serve buyer flow (the buyer never logs in to anything we own; the preview is the entire buyer-side experience).
- Inline payment / contracts.
- Multi-page generated sites (single-page is enough for the preview).

## Starting point: cloud-skeleton template

The implementation repo is **created from the GitHub template `reaandrew/cloud-skeleton`** ("Use this template" → "Create a new repository") and customized via `bin/init.sh`. The skeleton gives us, on day one:

- A Vite/React/TypeScript admin frontend, hosted via the skeleton's CloudFront+S3.
- A Go Lambda reference on `provided.al2023`, with shared `lambdas/pkg/`.
- Two-stack Terraform (`aws-setup/` singletons + `terraform/` per-env).
- API Gateway HTTP API with custom domain.
- DynamoDB single-table (`<project>-items[-<env>]`) with TTL on `expires_at`, PITR + deletion-protection prod-only.
- A BFF CloudFront with cookie→Authorization Header CFFn + Lambda@Edge router.
- **A fully isolated cloud preview environment for every PR**, automatically destroyed on PR close.
- A GitHub Actions pipeline gating every PR through lint + types + unit + secret-scan + SAST + SCA + IaC scan + Trivy + smoke + E2E + DAST + a11y + perf — all green, no advisory cope.
- 20 mitigated pitfalls (see `stdlib/skeleton-conventions.md`).

We add on top: extra DynamoDB GSIs, EventBridge bus + Scheduler, SES + suppression, Cognito user pool, Bedrock IAM, KMS for passcode cleartext, the agency-specific Lambdas in new `lambdas/<service>/` dirs sharing `lambdas/pkg/`, the Cloudflare R2 + Worker (with KV + Rate Limiting bindings) for **passcode-gated business-preview hosting**, and the agency UI screens.

We never modify the skeleton's pitfall mitigations.

## How to use this spec set

Read in order: 00 (this file) → 01 (architecture) → 04 (feedback) → 09 (iterations). Then 02–03 for data/events when you start implementing. 05 (capacity/cost), 06 (discovery), 07 (prompts), 08 (UI), 10 (quality) are referenced from iterations as you reach them. `stdlib/skeleton-conventions.md` is the reusable substrate ruleset.
