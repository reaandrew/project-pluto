# Claude Code instructions — website-agency

This is an AI-assisted outbound website-redesign pipeline. The implementation repo is **created from the GitHub template `reaandrew/cloud-skeleton`** and customized via `bin/init.sh`. This spec repo (`reaandrew/ai-website-agency-spec`) is the canonical source for the spec; copy `.ralph/` and `CLAUDE.md` into the implementation repo (`reaandrew/ai-website-agency`) on initialization.

Architecture, data, events, prompts, UI, iteration plan, and quality rules live under `.ralph/specs/`. Claude-based agents/commands/hooks live under `.claude/`.

## Current state — where to pick up (last updated 2026-05-09)

- **Iter 0.A → 0.E shipped.** All nine shared `lambdas/pkg/` libraries are merged on `main` of the impl repo: `events`, `idempotency`, `cost`, `bedrock`, `politefetch`, `schemas`, `prompts`, `passcode`, `killswitch`. Bootstrap (0.A.4–0.A.6) done; per-PR ephemeral envs working; CI efficiency rewrite (0.B) in place; agency-specific Terraform (0.C) live; Cloudflare R2 + Worker preview hosting (0.D) deployed.
- **Iter 0.F.1 in flight** — PR #16 on the impl repo: `terraform/pipeline-settings.tf` seeds the singleton `PipelineSettings` row (`pk=SETTINGS#PIPELINE, sk=CURRENT`) via `aws_dynamodb_table_item` with `lifecycle.ignore_changes=[item]`. `aws-setup/main.tf` was extended this session to grant the OIDC role `dynamodb:PutItem/GetItem/DeleteItem` (applied manually via `aws-vault exec personal_iphone -- terraform apply`). PR is green; awaiting merge.
- **Pick up at iter 0.F.2 next** — `lambdas/api-settings/`: `GET /settings`, `PATCH /settings`, operator-only via Cognito group claim. After that: 0.F.3 (kill-switch enforcement at every consumer entry), 0.F.4 (cost-ledger items + daily rollover Lambda at 00:05 UTC).
- After 0.F: **iter 0.G** (admin shell — `react-router-dom`, auth guard, Settings page with cap sliders, access-strip skeleton), then **iter 1** (Targeting Profile + Discovery — first real pipeline work).

## Operational gotchas worth remembering

- **IAM eventual-consistency lag.** Changes to `aws-setup/` IAM policies take 30–60s to propagate into newly assumed STS sessions. If a CI deploy fails with `AccessDenied` immediately after an `aws-setup/` apply, push an empty commit (`git commit --allow-empty -m "ci: retrigger after IAM propagation"`) to retrigger. Hit this on PR #16.
- **`aws-vault` is not invokable from Claude's Bash tool** (no TTY for the MFA prompt). For one-off `aws-setup/` applies, ask the user to type `! cd aws-setup && aws-vault exec personal_iphone -- terraform apply` so the prompt lands directly in their interactive session — the output streams back into the conversation.
- **PR titles must be Conventional Commits** (`feat:` / `fix:` / `chore:` / `docs:` / `ci:`) — the `conventional-title` GH Action is required and will block merge.
- **Three Go-lint rules that bite after-the-fact** (run `golangci-lint run --timeout=5m ./...` from `lambdas/` before pushing):
  - `errcheck` rejects bare error returns. Use `_ = fn(...)` to discard explicitly.
  - `semgrep` rejects `math/rand` everywhere. Use `crypto/rand` + `binary.LittleEndian.Uint64` for jitter / non-crypto randomness.
  - `semgrep` rejects `fmt.Fprintf` / `Fprintln` / `w.Write([]byte(...))` to `http.ResponseWriter`. Use `json.NewEncoder(w).Encode(v)` — the encoder hides the write behind the stdlib boundary.

## Read this first
- `.ralph/specs/00-overview.md` — what we're building and the operating principles.
- `.ralph/specs/01-architecture.md` — § "Starting point: cloud-skeleton template" + the stack table.
- `.ralph/specs/04-feedback-loops.md` — the three feedback loops (lead criteria, design, email).
- `.ralph/specs/09-iterations.md` — the 12-iteration plan, starting with iter 0 (skeleton bring-up).
- `.ralph/specs/11-agents.md` — the Claude-based agents, slash commands, and hooks.

Everything else (`02-data-model.md`, `03-events.md`, `05-capacity-and-cost.md`, `06-discovery-and-compliance.md`, `07-bedrock-prompts.md`, `08-admin-ui.md`, `10-quality-rules.md`, `stdlib/`) is referenced from those.

## Claude-based machinery
- `.claude/agents/` — four read-only review subagents (skeleton-pitfall, quality-rules, bedrock-prompt, cost-idempotency). Fired by the implementing-Claude before opening a PR.
- `.claude/commands/` — slash commands: `/review-iteration`, `/seed-vertical`, `/cost-burn`, `/explain-pitfall`.
- `.claude/settings.json` — hooks: destructive-AWS guard, substrate-edit warning, auto-`gofmt` on Go saves.
- `.github/pull_request_template.md` — enforces the spec's discipline (iteration ID, agents fired, pitfalls touched, capacity/cost/idempotency confirmations, demo evidence).

## When running under Ralph
`.ralph/PROMPT.md` is the operating manual. `.ralph/fix_plan.md` is the build queue. Pick the topmost unchecked item. Before opening a PR, run `/review-iteration` to fire the relevant review agents.

## When invoked directly (not in Ralph's loop)
Treat `.ralph/fix_plan.md` as the priority list, but do not produce the `---RALPH_STATUS---` block. Use a normal short summary at the end of the response.

## After opening a PR — watch CI

Opening the PR is not the end of the task. Once `gh pr create` returns the URL, poll `gh pr checks <num>` until every required check has settled (passed, failed, or skipped). If anything fails, read the failing log (`gh run view <run-id> --log-failed`), fix the root cause, push, and keep watching. Only stop watching once the PR is green or you have explicit confirmation from the user to leave it. Do not announce a task complete while checks are still running or failing — that's exactly when CI catches the things local lint/test missed (semgrep cloud rules, IAM propagation, etc.).

## AWS Authentication

For one-time bootstrap and emergency manual operations, always use `aws-vault` with the project's profile (set by `bin/init.sh --aws-vault-profile`):

```bash
aws-vault exec <profile> -- terraform apply
aws-vault exec <profile> -- aws sts get-caller-identity
```

In CI, AWS auth is via the OIDC role `github-actions-<project>` set up in `aws-setup/`.

## Hard rules
- Read `.ralph/specs/10-quality-rules.md` before producing any AI content. Non-negotiable: no fake testimonials, no fake awards, no claim-the-preview-is-published, no skipping `politeFetch` / `withCostCap` / idempotency, **no logging the passcode cleartext**.
- Skeleton pitfall mitigations are non-negotiable. See the impl repo's `docs/ARCHITECTURE.md` § Pitfalls table.
- Use **Go 1.24** for Lambdas (`provided.al2023`). Each service is `lambdas/<name>/` with `main.go` + a placeholder `bootstrap`.
- Use **Terraform 1.9** in two stacks: `aws-setup/` for singletons (manual, `aws-vault`) and `terraform/` for per-env (CI). Cloudflare resources in `cloudflare/`.
- Default Bedrock model is **Haiku 4.5**; only Sonnet 4.6 where `07-bedrock-prompts.md` calls for it.
- Region: `eu-west-2`.
- **Generated business previews** are hosted on Cloudflare R2 + a single Worker (with KV + Rate Limiting bindings) — passcode-gated. **Never** propose S3+CloudFront for these.
- The **admin app** is hosted on the skeleton's CloudFront+S3 (NOT Cloudflare Pages).
- The skeleton ships per-PR ephemeral envs. Use them for integration testing — do not run integration tests against a shared dev env.

## Build / test / deploy
See `.ralph/AGENT.md`.

## Do not modify
- `.ralph/` directory contents (PROMPT.md, fix_plan.md, AGENT.md, specs/).
- `.claude/` agents, commands, hooks (the system prompts and hook scripts are load-bearing — extend, don't rewrite).
- `.ralphrc`.
- The skeleton's `aws-setup/` files except where iteration 0 explicitly extends them.
- `scripts/derive-env-name.sh` — single source of truth for env-name derivation (skeleton pitfall #19).
- `bin/init.sh` — delete it after first use; never reuse.

These keep Ralph's loop alive and the substrate stable. Edits to the *content* of `fix_plan.md` and `AGENT.md` are expected (mark items complete; add new commands); deletion or restructuring is not.
