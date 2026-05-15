# Terraform resource-naming convention + acronym map

## Why this exists

AWS caps several resource-name lengths (EventBridge rule = 64, Lambda
function = 64, IAM role = 64, SQS queue = 80). Every per-env resource
name in this stack ends with `${local.env_suffix}` which is
`-` + `substr(env_sanitized, 0, 24)` — i.e. **up to 25 characters** of
suffix. Pitfall #8 (skeleton-pitfall-reviewer) fires when a verbose base
name + that suffix crosses the cap and breaks `terraform apply` on
long-named branches.

The cloud-skeleton project prefix `ai-website-agency` is an internal
handle and is **not** renamed in place (renaming live resources is
destructive — see CLAUDE.md). This convention therefore targets the
*variable* part of the name: the per-component tokens we add.

## Convention

- Budget the base name (everything before `${local.env_suffix}`) to
  **≤ 38 chars** so that base + 25-char max suffix ≤ 63 < 64.
- EventBridge rule names follow `<src>-to-<dst>` using the acronyms
  below. Prefer the acronym whenever the full word would push a name
  over budget; consistency beats cleverness — once a token has an
  acronym here, use the acronym everywhere new.
- Terraform resource *labels* (the HCL identifiers) stay
  human-readable; only the AWS `name = "..."` string is acronymised.
- Applies to **new** names. Existing deployed names are left as-is
  (renaming them forces destructive resource replacement); migrating
  them is a separate, deliberate decision — not done implicitly.

## Acronym map

| Acronym | Expansion | Notes |
|---------|-----------|-------|
| `web`   | website   | the generated preview site (the `Website` item) |
| `regen` | regenerate | operator-triggered re-render / passcode rotation |
| `req`   | requested | event-name suffix, e.g. `*.regenerate.requested` |
| `gen`   | generator | the `lambdas/generator` consumer |
| `pub`   | publisher | the `lambdas/publisher` consumer |
| `shot`  | screenshotter | the `lambdas/screenshotter` consumer |
| `spec`  | spec / spec-generator | context disambiguates (the Spec item vs the Lambda) |
| `dlq`   | dead-letter queue | already used project-wide |

When a new long token recurs in a name, add a row here in the same PR
that introduces it, with a one-line note on what it expands to.

## Examples

- `web-regen-to-gen${local.env_suffix}` — routes
  `website.regenerate.requested` → the generator SQS queue (iter 5.6b).
  Base = 18 chars; + 25 max suffix = 43 ≤ 63. ✓
- Existing `spec-approved-to-generator${local.env_suffix}` predates this
  convention and is left in place (live resource; non-destructive policy).
