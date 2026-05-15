# Claude Code instructions — website-agency

This is an AI-assisted outbound website-redesign pipeline. The implementation repo is **created from the GitHub template `reaandrew/cloud-skeleton`** and customized via `bin/init.sh`. The spec repo (`reaandrew/ai-website-agency-spec`) is the canonical source for the spec; copy `.ralph/` and `CLAUDE.md` into the implementation repo (`reaandrew/project-pluto`) on initialization. (AWS resource names, Go module path, and SSM parameter paths still carry the original `ai-website-agency` identifier — renaming them in place is destructive; treat that prefix as an internal handle.)

Architecture, data, events, prompts, UI, iteration plan, and quality rules live under `.ralph/specs/`. Claude-based agents/commands/hooks live under `.claude/`.

## Current state — where to pick up (last updated 2026-05-15)

- **Iters 0 → 4 fully shipped + merged on `main`.** Skeleton bring-up (0.A–0.G), Targeting+Discovery (1), Audit engine (2: technical pre-audit, Bedrock qualitative, audit consumer), Qualification+Backlog (3: priorityScore, qualifier, backlog-promoter), Spec generator (4: VerticalStyleGuide, `spec.v1` Sonnet prompt+wrapper, spec-generator Lambda, spec review BFF + `/queue/[id]` UI + `feedback.captured` capture path). Big iters were split into stacked PRs (Na/Nb) per the established convention.
- **Iter 5 in progress** (Component Site Generator + R2 Publish + Passcode):
  - **5.1 merged** — `lambdas/pkg/components/` Go-template renderers for all 7 section kinds + Footer.
  - **5.2 merged** — `lambdas/pkg/sitebundle/` (Spec → self-contained HTML, inline CSS) + `lambdas/generator/` consumer of `spec.approved` → S3 bundle + `website.generated`.
  - **5.3 merged** (PR #45) — `lambdas/publisher/`: S3→R2 copy, 8-char Crockford-Base32 passcode, hash→KV, KMS-encrypt cleartext→`Website.passcodeCipher`, `passcodeRevealableUntil=now+7d`, `website.published` (no cleartext). 7 new SSM SecureString placeholders; `aws-setup/main.tf` ssm_access allowlist extended with `/r2/* /cloudflare/* /passcode/* /preview/*` and applied via `aws-vault`.
  - **5.4 merged** (PR #46) — Worker enforces passcode-revocation propagation <60s (KV freshness check on every cookie-auth'd `/sites/*` + `/screenshots/*` request, per-isolate 60s cache). **argon2id swap deferred as a tracked follow-up** (documented inline in `worker/src/passcode.ts` + `lambdas/pkg/passcode/passcode.go`): Cloudflare `vitest-pool-workers` blocks dynamic `WebAssembly.compile()` that `hash-wasm` needs; both sides stay on cross-pinned SHA-256 until a wrangler `wasm_modules` preload (or worker-pool-free test path) lands.
- **Pick up next: iter 5.5** — screenshot job via Cloudflare Browser Rendering API + operator-mode bypass token. Then **5.6** (site preview Approve/Reject/Regenerate → `feedback.captured`; regenerate issues a fresh passcode, revokes old). Then **iters 6–11** (Review Queue, Contact enrichment, Email engine, Tuners, Production hardening, Launch).
- **Canonical progress tracker is `.ralph/fix_plan.md`** — each completed item carries a detailed `*(done …)*` annotation. Read it first; this section is the short pointer.

## Operational gotchas worth remembering

- **IAM eventual-consistency lag.** Changes to `aws-setup/` IAM policies take 30–60s to propagate into newly assumed STS sessions. If a CI deploy fails with `AccessDenied` immediately after an `aws-setup/` apply, push an empty commit (`git commit --allow-empty -m "ci: retrigger after IAM propagation"`) to retrigger. Hit this on PR #16.
- **`aws-vault` IS invokable from Claude's Bash tool when the operator has a fresh MFA session cached.** Run `cd aws-setup && aws-vault exec personal_iphone -- terraform apply` directly. If MFA has expired the command fails with `Failed to get credentials … EOF`; in that case commit + push the `aws-setup/` change and ask the operator to apply it (or re-supply MFA and retry). `aws-setup/` IAM/SSM changes are *not* applied by CI — only the per-env `terraform/` stack is — so a new SSM path family or IAM action needs this manual apply before the PR's deploy stage can pass. This recurs every few iters (iter 1.3 `/providers/*`, iter 2.3 `lambda:CreateEventSourceMapping`, iter 5.3 `/r2/* /cloudflare/* /passcode/* /preview/*`).
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

**Standing directive — always proceed, never ask "shall I start?".** When the user asks "what's next", asks for an iteration-plan review, or otherwise signals it's time to move forward, the expected behaviour is: identify the topmost unchecked `.ralph/fix_plan.md` item and **immediately begin implementing it** — feature branch → implement → `/review-iteration <iter-id>` → `gh pr create` → watch CI to green. Do **not** stop to ask for confirmation to start the next iteration; that confirmation is pre-granted. Only pause for genuinely destructive or human-only steps (e.g. `aws-vault`/DNS bootstrap items explicitly marked **HUMAN ONLY**). This is a durable preference, not a per-session one.

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
