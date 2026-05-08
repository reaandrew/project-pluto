# 10 — Quality Rules (Non-Negotiables)

These rules apply to every artifact the system produces — audits, specs, sites, emails, tuner deltas. Violating any one is a hard fail; we'd rather ship nothing than ship a fabrication.

## Rule 1 — No invented business facts

The system must NEVER produce or include:

- Testimonials (real or labelled).
- Awards, "as featured in", press logos.
- Staff names, photos, or biographies the source site does not display.
- Specific customer counts, statistics, or success metrics.
- Prices, fees, packages, or "from £X" claims.
- Certifications, qualifications, regulatory bodies, or affiliations.
- Industry rankings, ratings, or "5-star Google".
- Years in business, founding dates, or "since 19XX".
- Service guarantees ("100% satisfaction", "money-back").
- Locations, addresses, or service areas not present in the source.
- Telephone numbers or email addresses other than those visible on the source site.

Enforcement: post-validators on the spec generator and email generator strip these categories of content. The renderer drops any Trust badge whose label is not present verbatim in the source HTML. The email post-validator rejects any draft containing `\d+%`, `since \d{4}`, `\d+\s+(years?|customers?|clients?)`, "guaranteed", "award", "winner", or other listed patterns unless the same string is present in the source site or the operator's manual edit.

## Rule 2 — The preview is private (and passcode-gated)

Every page served from the Worker carries:

```
X-Robots-Tag: noindex, nofollow
Cache-Control: public, max-age=300, s-maxage=86400   (R2 content)
Cache-Control: no-store                               (passcode form page)
```

Previews are not linked from any indexable property we own. The preview URL pattern includes a UUID (not the business name) so it isn't guessable. We never publish a preview URL on a public page.

In addition, every preview is **passcode-gated** by the Worker:

- Each `Website` has a unique 8-char Crockford-Base32 passcode (no `I/L/O/U`).
- Worker stores **only** the argon2id hash in Workers KV; cleartext lives KMS-encrypted on the `Website` item with `passcodeRevealableUntil` 7 days out, then wiped.
- Worker enforces 5 attempts per IP per websiteId per 10 minutes; 429 thereafter.
- Worker uses constant-time hash comparison and a signed cookie for repeat visits.
- A revoked passcode (`passcodeRevokedAt` set) is rejected by the Worker within 60s of revocation.
- The cleartext passcode is **never** logged, traced, emitted in events, or stored unencrypted at rest. Any new code path that handles the cleartext must be reviewed against this rule explicitly.

## Rule 3 — Outreach is honest

The email and the entire outbound flow must:

- **Identify the sender truthfully** — From: name and reply address are real.
- **Identify the company** — Footer includes our company name and postal address.
- **Describe the preview accurately** — phrasing must clearly convey it is a private mockup, not a deployed redesign:
  - Acceptable: "I made a private preview showing how X could look", "mocked up an alternate layout", "a sketch of how the site could look".
  - Forbidden: "We redesigned your site", "your new site is live", "I rebuilt it for you", "as you requested", "thanks for asking us to look at this".
- **Make opt-out trivial** — `List-Unsubscribe` header (RFC 8058 one-click) plus a free-text "reply 'no thanks'" line. Honored within 24 hours.
- **Use a non-corporate subdomain** — outreach goes from `outreach.<domain>`, not the corporate domain.

The email post-validator scans for forbidden phrasings. The CI also has a static check that any new email template fixture passes the post-validator.

## Rule 4 — All AI outputs are structured

No free-text JSON parsing. Every Bedrock call uses tool use with a typed schema (defined in `packages/contracts/schemas/`). If the model fails to produce valid tool use after 2 retries, the handler errors out and the work goes to the DLQ.

## Rule 5 — Every paid call is capacity- and cost-gated

Every Lambda that calls Bedrock or any paid API:
1. Reads `pipelineEnabled` and the relevant stage flag, and returns success without acting if either is false.
2. Wraps the paid call in `withCostCap(stage, estimateUsd, fn)`.
3. Caches outputs via `(promptId, inputHash)` with TTL.

A static CI check fails the build if a `BedrockClient` instance is constructed outside `services/lib/bedrock-cost-aware.ts`.

## Rule 6 — Every consumer is idempotent

Every event handler:
1. Conditionally writes `IDEMP#<eventId>` with `attribute_not_exists(pk)`.
2. On `ConditionalCheckFailedException`, returns success without acting.
3. Is otherwise pure with respect to `(eventId)` — replaying an event produces the same artifact (same id, same content) or no-op.

Replay tests in CI re-fire each event into each consumer twice; both the artifact count and the side-effect-call count must remain unchanged.

## Rule 7 — Operator overrides are sacred

The operator's edits to a Profile (Targeting / Style / Tone) are the source of truth. The tuner system NEVER overwrites a manual edit. Tuners only operate against the current `version`; if the version moves between proposal and apply (because the operator edited), the apply is rejected and the operator sees a "stale delta" warning.

## Rule 8 — Source site fetches are polite

Every outbound fetch goes through `politeFetch`:
- Reads and respects `robots.txt`.
- Honors `Crawl-delay`.
- Sets a real `User-Agent` with a contact URL.
- Throttles to 1 req per host per 5 seconds globally.
- Backs off on 429/5xx with jitter.

A unit test on `politeFetch` covers each of these. A handler that imports `fetch` directly without going through `politeFetch` fails the build.

## Rule 9 — PII is minimized

We collect:
- Business name, domain, location, vertical.
- Director name, role, business email, derived confidence.
- Optional: contact phone (only if present on source site).

We do NOT collect:
- Personal phone numbers (unless explicitly business-listed).
- Home addresses.
- Copies of the source site's customer data.
- Anything from a contact form's hidden fields.

The DPA-relevant fields have a 12-month retention default; right-to-erasure is honored by `/admin/erasure` (writes a tombstone, scrubs the email and name, keeps an aggregated metrics record).

## Rule 10 — Visual quality bar

A generated preview that scores Lighthouse `< 90` performance or `< 90` accessibility on the synthetic CI render is not allowed to publish. The publisher checks; on fail, it emits `website.generation.failed` instead of `website.published` and the operator sees a regenerate prompt with the specific scores.

## Rule 11 — Tests don't lower the bar

If a test would fail because a quality rule is being honored, fix the implementation, not the rule. CI is configured to make these tests un-skippable (`vitest --no-skip`), and `it.skip` on a quality-rule test fails CI.

## Rule 12 — Document the WHY in commits, not in comments

Inline comments are reserved for non-obvious constraints (e.g., "Bedrock rejects max_tokens > 4096 for Haiku", "List-Unsubscribe header order matters for Gmail"). General rationale lives in the commit message and PR description so it isn't repeated in every file that touches a concept.

## Rule 13 — Skeleton mitigations are non-negotiable

The cloud-skeleton template's `docs/ARCHITECTURE.md` § Pitfalls table lists 20 mitigations baked into the substrate. Any code or terraform change that would re-introduce one of those pitfalls (e.g., adding a singleton to `terraform/`, adding a per-table IAM policy, removing a `bootstrap` placeholder, hardcoding a domain literal, widening the 31-char env-name cap, removing `lifecycle.ignore_changes` from a managed-out-of-band SSM parameter) is a hard fail. CI lints for these specifically; the lints are not skippable.

## Audit hooks

Every artifact and every operator decision is appended to a tamper-evident log (`Feedback` and `EmailEvent` items, plus daily checksum manifests written to S3 and the DynamoDB stream). For any ~1-year period we can produce a complete history of "this was generated by these inputs, the operator did X with it, and we sent the resulting email".

This isn't a regulatory requirement at MVP scale, but it is the precondition for ever scaling — and operationally cheap.
