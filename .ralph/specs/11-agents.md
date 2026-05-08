# 11 — Claude-based Agents & Utilities

This spec describes the Claude-based machinery in this project: dev-time review subagents, slash commands, hooks, and one production agent.

The substrate (Claude Code subagents under `.claude/agents/`, slash commands under `.claude/commands/`, hooks in `.claude/settings.json`) is shipped in this spec repo and copied verbatim into the implementation repo on `bin/init.sh` follow-up. The production agent is a deployed Claude Agent SDK service added in iter 8.5+.

---

## Dev-time review subagents

These run during Ralph's loop or when the operator triggers `/review-iteration`. Read-only. Each lives in `.claude/agents/<name>.md`.

### 1. `skeleton-pitfall-reviewer`
- **Fires when**: any change in `terraform/`, `aws-setup/`, `cloudflare/`, `lambdas/<svc>/`, `.github/workflows/`, or any cleanup script.
- **Job**: cross-check the diff against the 20 mitigated pitfalls in `stdlib/skeleton-conventions.md`. Flags violations by pitfall number with file:line.
- **Why it earns its keep**: the substrate is the result of 6 production attempts; reintroducing a pitfall is the most common silent regression.

### 2. `quality-rules-enforcer`
- **Fires when**: any AI-generated artifact (Spec, rendered Website HTML, EmailDraft body, tuner Delta) is about to be approved or persisted.
- **Job**: verify the artifact complies with every rule in `10-quality-rules.md`. Flags fake-fact violations, passcode-in-logs leakage, broken outreach honesty rules, missing opt-out lines, etc.
- **Why it earns its keep**: humans miss subtle violations; CI mechanical checks miss semantic ones.

### 3. `bedrock-prompt-reviewer`
- **Fires when**: any change in `lambdas/pkg/prompts/` or `lambdas/pkg/schemas/`.
- **Job**: verify the prompt + tool-use schema + post-validator + snapshot test + contract test + adversarial tests + cost-class declaration form a complete set per `07-bedrock-prompts.md`.
- **Why it earns its keep**: prompts are easy to ship under-tested; this agent makes the completeness checklist mechanical.

### 4. `cost-idempotency-reviewer`
- **Fires when**: any new or changed `lambdas/<svc>/main.go`.
- **Job**: verify handler entry implements (in order) kill switch → idempotency → cost cap on paid calls → polite fetch on outbound HTTP → `expires_at` on cache writes.
- **Why it earns its keep**: each rule is a one-line check at handler entry, but a single missing one can produce runaway cost or a duplicate-send incident.

These four agents are explicitly NOT empowered to write code. The implementing-Claude (Ralph or a human-driven session) addresses every flag they raise.

---

## Slash commands (operator + dev shortcuts)

Live under `.claude/commands/`. Invocation: `/<name> <args>`.

### `/review-iteration [N]`
Fires the relevant subset of the four review agents on the current branch's diff, in parallel. Prints per-agent verdicts and a consolidated PASS/FAIL. Used by the implementing-Claude before opening a PR.

### `/seed-vertical <vertical-name>`
Generates draft `TargetingProfile`, `VerticalStyleGuide`, and `EmailToneProfile` for a new vertical. Output is JSON the operator pastes (or a follow-up Claude writes via the BFF API after operator approval). Stylistic-only — never invents business facts.

### `/cost-burn [YYYY-MM-DD | last7]`
Reads the cost ledger and `PipelineSettings.budgets`, prints a one-screen table with per-stage spend, calls, cap, headroom, and any auto-paused stages. Operator's daily-driver for cost visibility outside the dashboard.

### `/explain-pitfall <N>`
Prints the full context (past failure + mitigation + code references) for cloud-skeleton pitfall N. Used during PR review when Ralph or the operator wants to confirm a mitigation is still intact.

---

## Hooks (in `.claude/settings.json`)

Hooks fire automatically — they are the harness's safety rails, not Claude's.

### `PreToolUse: Bash` — destructive-AWS guard
Blocks any `terraform destroy` (or `terraform apply -destroy`) targeted at `production`, `main`, `master`, `prod`, or `develop`, and any deletion of the production frontend or Terraform state bucket. Defense in depth on top of the skeleton's own cleanup script denylist (pitfall #13).

### `PreToolUse: Edit|Write` — substrate-edit warning
Asks for explicit confirmation before editing `aws-setup/`, `.ralph/PROMPT.md`, `.ralph/specs/`, `.ralphrc`, or `scripts/derive-env-name.sh`. Prevents accidental restructure of the substrate during routine iteration work.

### `PostToolUse: Edit|Write` — auto-`gofmt`
Runs `gofmt -w` on any `.go` file after Claude edits it. Keeps the Go tree always-formatted; the skeleton's CI `gofmt` check then becomes a no-op in the happy path.

---

## Sub-directory `CLAUDE.md` files (added by Ralph in iteration 0.A.7)

Each directory gets a focused `CLAUDE.md` so Ralph loads the right context for the area it's working in:

- **`lambdas/CLAUDE.md`** — Go conventions, error wrapping with `%w`, no panics in handlers, the five entry-of-handler rules (kill switch / idempotency / cost cap / polite fetch / cache TTL).
- **`terraform/CLAUDE.md`** — pitfall reference, `local.env_suffix` pattern, IAM grouping, no hardcoded domains.
- **`worker/CLAUDE.md`** — edge runtime constraints, Miniflare for tests, no Node APIs, signed-cookie format.
- **`frontend/CLAUDE.md`** — Vite `base: './'` is load-bearing, runtime-config.js pattern, never import Node-only modules in route handlers.

These are stubs Ralph fills in during iteration 0; they don't duplicate the spec — they reference it ("read `.ralph/specs/<file>` for X").

---

## Production Claude Agent — `reply-triage`

Added in iteration **8.5** (between current iter 8 SES sending and iter 9 feedback). NOT a dev-time subagent — a deployed Claude Agent SDK service.

### Purpose
When an inbound reply lands at the SES inbound rule's S3 bucket, classify it and route the candidate's state.

### Inputs
- The reply email (raw RFC 822) — sender, subject, body, In-Reply-To.
- The candidate's `Business`, `EmailDraft`, and `Audit` for context.

### Output
A structured tool-use response:

```json
{
  "category": "positive_interest | negative | unsubscribe | wrong_person | autoresponder | unknown",
  "confidence": 0.0,
  "summary": "<1-line>",
  "suggestedAction": "respond | suppress | mark_responded | ignore | escalate"
}
```

### Wiring
- Lambda triggered by S3 PUT on the SES inbound bucket.
- Calls Bedrock Haiku 4.5 via the existing `pkg/bedrock` client.
- Writes the classification to a new `EmailEvent` (`event=replied,category=...`).
- If `category=unsubscribe` (high confidence ≥ 0.8): adds the address to suppression and updates `Business.status='rejected_after_review'`.
- If `category=positive_interest`: marks `Business.status='responded'` and surfaces in a new `/replies` view.
- If `confidence < 0.6`: marks `unknown`, surfaces to the operator with the raw email for manual classification.

### Cost
~5K tokens in / 0.3K out @ Haiku ~$0.0055/reply. Negligible at MVP volume.

### Why an agent and not just a Lambda + prompt
Operator preference and conversation continuity. A single Bedrock call is fine for the classification, but the **Agent SDK** wrapper makes it trivial to add follow-on capabilities later: drafting a polite reply for the operator to review, summarising a thread, etc. Start with the simplest classification; let the agent shape grow with use.

---

## Optional / not adding now

These would be plausibly valuable but premature at MVP:

- **`audit-second-opinion` agent** — re-runs borderline audits (`worthRedesigning` confidence 0.45–0.65) with a different system prompt to break ties. Add if Iter 2 audit accuracy proves a problem.
- **`email-pre-send` agent** — a final semantic check before SES handoff (the post-validator already does the mechanical checks). Add if any incident reveals a category of bad output the post-validator misses.
- **`tuner-delta-sanity` agent** — a second-look on tuner-proposed deltas before they reach `/tuners`. The operator already gates them; adding an agent in the loop is operator-preference, not necessity.

If any of these become necessary, add the spec entry here and the `.claude/agents/<name>.md` file in the same PR.
