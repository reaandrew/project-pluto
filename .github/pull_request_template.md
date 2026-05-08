<!--
The PR template enforces the spec's discipline. Fill every section. If a section
is N/A, write "N/A — <one-line reason>". Don't delete sections.
-->

## Iteration

- Iteration: <!-- e.g., 5.3 publisher Lambda -->
- `.ralph/fix_plan.md` items closed by this PR: <!-- e.g., - [x] 5.3 publisher Lambda -->

## Summary

<!-- 1–3 sentences. What changed and why. Reference the spec section that drove it. -->

## Reviews fired

<!-- Tick the agents that ran via /review-iteration. PASS/FAIL per agent. -->

- [ ] `skeleton-pitfall-reviewer` — PASS / FAIL
- [ ] `cost-idempotency-reviewer` — PASS / FAIL
- [ ] `bedrock-prompt-reviewer` — PASS / FAIL / N/A
- [ ] `quality-rules-enforcer` — PASS / FAIL / N/A

## Skeleton pitfalls touched

<!-- For each pitfall whose mitigation this PR brushes against, write the number
     and explain how the mitigation is preserved. If none, write "None". -->

- Pitfall #X — preserved by …

## Capacity, cost, idempotency

- New paid-API call sites in this PR (Bedrock/Places/SES/etc.): <!-- list, or "none" -->
- All wrapped in `withCostCap`? <!-- yes / no -->
- All cached in DynamoDB with `expires_at`? <!-- yes / no / N/A -->
- New event consumers in this PR: <!-- list, or "none" -->
- All wrapped in `WithIdempotency` at handler entry? <!-- yes / no / N/A -->
- New outbound HTTP call sites: <!-- list, or "none" -->
- All routed through `politefetch`? <!-- yes / no / N/A -->

## Quality rules

- Fake-fact safety post-validators added/updated? <!-- yes / no / N/A -->
- Passcode cleartext: not in events / logs / X-Ray / feedback log? <!-- confirmed / N/A -->

## Tests

- Unit:
- Integration:
- E2E (Playwright on the per-PR preview env):
- Snapshot tests for any prompt change:
- Adversarial tests for any post-validator:
- Coverage on new code: <!-- ≥ 85% required -->

## Demo

<!-- Per the project's "every iteration ends with a working demo" rule.
     Paste the per-PR preview URL and one or two curl/screenshot evidence lines
     showing the new behavior works end-to-end. -->

- Preview URL: https://preview.<base>/<env>/
- Evidence:

## Spec & docs

- [ ] `.ralph/fix_plan.md` updated (boxes ticked, follow-ups added)
- [ ] `.ralph/AGENT.md` updated if commands changed
- [ ] `docs/` updated if behaviour the operator should know about changed

## Rollback

<!-- Single line: how to revert this PR safely. Default is "revert this PR";
     callout if any data migration / SSM placeholder import / KV write needs
     undoing manually. -->
