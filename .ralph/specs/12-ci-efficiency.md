# 12 — CI Efficiency

The cloud-skeleton's CI pipeline (`.github/workflows/deploy.yml`, ~588 lines) is correct and comprehensive but runs more than it needs on every PR. This spec lists the changes Ralph makes during iteration **0.B** to cut wall-clock time on common PR shapes (doc PRs, frontend-only PRs, lambda-only PRs) without weakening any quality gate.

The principle is **path-aware execution**: run a job when the change affects what the job tests, skip it otherwise. No quality gate is removed; gates are reorganized so they fire only on relevant code paths.

## Wins (ordered by minutes saved per PR)

### 1. Doc-only PR fast lane (~15–20 min saved per doc PR)

A doc-only PR — only `**.md`, `.ralph/**`, `docs/**`, `.github/pull_request_template.md`, `.github/CODEOWNERS` — must NOT spin up a per-PR ephemeral env. The Playwright + ZAP + Lighthouse + k6 + post-deploy-seed chain is wasted work for a typo fix.

**Implementation**:

- Add a top-of-pipeline `changed-files` job (using `dorny/paths-filter@v3`) that produces outputs `frontend_changed`, `lambdas_changed`, `terraform_changed`, `cloudflare_changed`, `worker_changed`, `code_changed` (= any of the above). Anything that's not under those paths is treated as docs.
- Gate `deploy` on `code_changed == 'true'`. The `if:` condition keeps the existing `pull_request.action != 'closed'` check AND adds the path filter.
- Gate `smoke-tests`, `post-deploy-seed`, `e2e-tests`, `dast-zap`, `accessibility-lighthouse`, `load-test-k6` on `code_changed == 'true'` (they all `needs: deploy`).
- Lint/test/scan jobs stay path-filtered too — see win #2.

**Acceptance**: PR with only `README.md` changed: pipeline runs only the `changed-files` job + secret-scan (~2 min total). PR with a Lambda change: full pipeline.

### 2. Path-filter the heavy jobs (~3–8 min saved on most PRs)

Same `changed-files` job feeds the rest:

| Job | New `if:` |
|---|---|
| `lint-frontend` (merged with `test-frontend-unit` per win #4) | `frontend_changed == 'true'` |
| `lint-go` (merged with `test-go-unit`) | `lambdas_changed == 'true'` |
| `lint-terraform` | `terraform_changed == 'true'` |
| `iac-scan` | `terraform_changed == 'true'` |
| `discover-lambdas` + `build-go-lambdas` matrix | `lambdas_changed == 'true'` |
| `build-frontend` | `frontend_changed == 'true'` |
| `build-worker` (new in iter 0.C) | `worker_changed == 'true' \|\| cloudflare_changed == 'true'` |
| `dast-zap`, `accessibility-lighthouse` | `frontend_changed == 'true' \|\| terraform_changed == 'true'` |
| `load-test-k6` | `lambdas_changed == 'true' \|\| terraform_changed == 'true'` |
| `secret-scan` | always — diff is small, runtime is bounded |

**Note on `deploy`**: It can run with **only** Terraform changes (no Lambda or frontend rebuild) — in that case the artifact-download steps are conditional on the matching build job's success. Leave the existing `if:` on `Place Lambda binaries` and `Deploy frontend` steps so they no-op if the upstream build was skipped.

**Acceptance**: A `terraform/`-only PR doesn't trigger the Lambda matrix or the frontend build. A `lambdas/`-only PR doesn't trigger the frontend build or `dast-zap`.

### 3. Move advisory scans off the per-PR critical path (~2–3 min saved per PR)

`sca` (govulncheck + `npm audit` + `dependency-review-action`) and the `iac-scan` job both run with `continue-on-error: true` / `soft_fail: true` — they're advisory until GHAS is enabled. Running them on every PR adds wall-clock for output the operator already gets out-of-band.

**Implementation**:

- Move `sca` to `security.yml` triggered on:
  - `schedule: cron: '0 6 * * 1'` (Monday 06:00 UTC weekly)
  - `pull_request: paths: ['**/go.mod', '**/go.sum', '**/package*.json']`
  - `workflow_dispatch`
- Keep `iac-scan` in `deploy.yml` BUT gate on `terraform_changed == 'true'`. Once GHAS is enabled and findings become blocking, this stays in the per-PR path; until then, the path filter is enough.
- `secret-scan` stays in the per-PR path (it's blocking and fast).

**Acceptance**: `sca` does not appear in the checks list on a PR that doesn't change dep manifests; it appears once a week on the scheduled run and reports findings.

### 4. Merge same-setup jobs (~30–90 s saved)

Three jobs each `npm ci` independently (`lint-frontend`, `test-frontend-unit`, `build-frontend`). Two jobs each `setup-go` independently (`lint-go`, `test-go-unit`).

**Implementation**:

- **Frontend**: combine `lint-frontend` + `test-frontend-unit` into one job `frontend-quality` that does `npm ci`, then `typecheck`, `lint`, `format:check`, `test:coverage` in sequence. Codecov upload at the end. `build-frontend` keeps its own `npm ci` (different job, separate cache layer) but uses the npm action's cache so the install is fast.
- **Go**: combine `lint-go` + `test-go-unit` into one job `go-quality`. One `setup-go`, one checkout, runs `gofmt`/`go vet`/`golangci-lint`/`go test -race -coverprofile=…`. Codecov upload at the end.

**Acceptance**: PR check list shows `frontend-quality` and `go-quality` instead of four separate jobs; cumulative wall-clock drops by 30–90 s.

### 5. Cache Terraform providers (~10–30 s per `terraform init`, twice per PR)

`terraform init` downloads the AWS provider afresh every time. Provider download is one of the biggest hidden costs on Terraform pipelines.

**Implementation**:

```yaml
env:
  TF_PLUGIN_CACHE_DIR: ${{ runner.temp }}/.terraform.d/plugin-cache
steps:
  - run: mkdir -p "$TF_PLUGIN_CACHE_DIR"
  - uses: actions/cache@v4
    with:
      path: ${{ env.TF_PLUGIN_CACHE_DIR }}
      key: tf-providers-${{ hashFiles('**/.terraform.lock.hcl') }}
      restore-keys: tf-providers-
```

Apply to every job that runs `terraform init`: `lint-terraform`, `deploy`, `cloudflare-deploy` (in iter 0.C).

**Acceptance**: Cold cache: providers downloaded once, cached. Warm cache: `terraform init` completes in < 5 s instead of ~20–40 s.

### 6. Drop `closed` from `deploy.yml`'s PR trigger (cleanup)

The `pull_request: types: [opened, synchronize, reopened, closed]` includes `closed`. The current jobs each have `if: github.event.action != 'closed'` to skip body, but the workflow is still scheduled and the `concurrency` group counter ticks. `destroy.yml` separately handles `closed` for cleanup.

**Implementation**: In `deploy.yml` change to `pull_request: types: [opened, synchronize, reopened]`. Remove the now-redundant `action != 'closed'` clauses from per-job `if:` lines.

**Acceptance**: Closing a PR triggers only `destroy.yml`, not `deploy.yml`.

### 7. Shallow checkout for diff-only scans

`secret-scan` uses `fetch-depth: 0` (full history). Trufflehog with explicit `base`/`head` SHAs scans a diff and works with shallow clones.

**Implementation**: change `fetch-depth: 0` to default (1) when `pull_request`; keep `0` only when scanning the full main history (e.g., on push-to-main where there's no PR diff, but even then we can scan a recent window).

**Acceptance**: Secret scan completes in ~5 s on small diff PRs.

### 8. Reusable workflow / composite action for the giant `if:`

Every job carries `if: github.event_name != 'create' && (github.event.action != 'closed' || github.event_name != 'pull_request')`. Centralize as a `pre-flight` job that emits `outputs.run` once; downstream jobs do `if: needs.preflight.outputs.run == 'true'`. Combine with the `changed-files` outputs from win #1.

**Implementation**: New first job `pre-flight` produces:

```yaml
outputs:
  run:               ${{ ... }}    # = combination of (not create) and (not pull_request closed)
  frontend_changed:  ${{ ... }}
  lambdas_changed:   ${{ ... }}
  terraform_changed: ${{ ... }}
  cloudflare_changed:${{ ... }}
  worker_changed:    ${{ ... }}
  code_changed:      ${{ ... }}
```

All downstream `if:` lines reference `needs.pre-flight.outputs.<name>`. Maintenance becomes one place to edit instead of dozens.

**Acceptance**: `if:` clauses on per-job lines are one line each, reading from `pre-flight` outputs.

### 9. Branch fast lane (~5–10 min saved per branch push) — fix_plan 0.B.12

Pushing to a feature branch shouldn't spin up a per-PR ephemeral env. The dev iteration loop is "code → push → wait for lint/test → fix" — the per-PR env exists for *humans reviewing the PR*, not for the developer's own pre-PR feedback.

**Implementation**:

- Broaden the `push` trigger to all branches: `push: { branches: ['**'] }` (was `[main]`).
- Drop the `create:` event entirely; with `push: '**'` covering every branch's first commit, the `create` event is redundant.
- Delete the `provision-branch` job (its `if: github.event_name == 'create'` no longer fires; the env-name template substitution is implicitly validated by the deploy job on PR open).
- Gate the heavy chain on `pull_request` events or `push` to `main` only:
  - `deploy`, `smoke-tests`, `post-deploy-seed`, `e2e-tests` use:
    `(github.event_name == 'pull_request') || (github.event_name == 'push' && github.ref == 'refs/heads/main') || github.event_name == 'workflow_dispatch'`
  - `dast-zap`, `accessibility-lighthouse`, `load-test-k6` are `pull_request`-only (their value is in PR review, not the developer loop).

What runs on each event after this change:

| Trigger | What runs |
|---|---|
| `push` to feature branch | `pre-flight`, `secret-scan`, `go-quality` (if lambdas changed), `frontend-quality` (if frontend changed), `lint-terraform` (if tf changed), `iac-scan` (if tf changed), `discover-lambdas`, `build-go-lambdas`, `build-frontend` |
| `pull_request` (open / sync) | All of the above + `deploy` + `smoke-tests` + `post-deploy-seed` + `e2e-tests` + `dast-zap` + `accessibility-lighthouse` + `load-test-k6` |
| `push` to `main` | All of `pull_request` set, applied with `environment=production` |

**Acceptance**: A push to a feature branch produces no `deploy` / `e2e` / `dast` / `lighthouse` / `k6` checks. Opening a PR for that same branch triggers them. Wall-clock for a Go-only branch push is `pre-flight` + `secret-scan` + `go-quality` + `discover-lambdas` + `build-go-lambdas` only — typically < 2 min.

## Smaller / marginal — implement only if a measurable problem appears

- **Codecov uploads** — `continue-on-error: true` already so they don't block. Async with `& wait` if upload itself shows up in flame graphs.
- **Per-Lambda build cache** — already cache-keyed on src + pkg + go.mod hash. A pkg change correctly invalidates all Lambdas; that's not a bug.
- **`terraform fmt -check` runs in two places** — `lint-terraform` and `deploy` (advisory). Drop the duplicate from `deploy`.
- **ZAP / Lighthouse / k6 each install Node from scratch** — small; only meaningful if their CI minutes show up in the burn rate.
- **Docker pull retries** (skeleton pitfall #15) are unconditional; in practice they almost always succeed first try, so the 5×15s loop is theoretical headroom. Don't change it.

## What NOT to change

- **Skeleton's quality gates themselves.** Lint, types, unit tests, secret scan, IaC scan, smoke, E2E, DAST, a11y, perf — all stay. We're path-aware about *when* they fire, never about *whether* they fire on relevant changes.
- **Per-PR ephemeral env model.** This is the skeleton's killer feature; the goal is to reach it faster, not skip it.
- **`destroy.yml`** — leave alone. Cleanup is its own concern.
- **The four review subagents** (skeleton-pitfall, quality-rules, bedrock-prompt, cost-idempotency) — they live in Claude Code, not CI. Don't move them into CI; they earn their keep at PR-creation time, before CI runs.

## Project-specific CI utilities

These are nice-to-have additions on top of the path-filter wins:

### `make ci-local`

A target in the implementation repo's top-level `Makefile`:

```makefile
ci-local:
	cd lambdas && gofmt -l . | (! grep .) && go vet ./... && golangci-lint run --timeout=5m && go test -race ./...
	cd frontend && npm ci && npm run typecheck && npm run lint && npm run format:check && npm run test:coverage
	cd worker && npm ci && npm run typecheck && npm run lint && npm run test
	cd terraform && terraform fmt -recursive -check && terraform validate
	cd aws-setup && terraform fmt -recursive -check && terraform validate
	cd cloudflare && terraform fmt -recursive -check && terraform validate
	gitleaks detect --no-banner
```

Lets the operator (or Ralph in interactive mode) catch fmt / vet / lint / test failures before pushing — saves a ~12 min CI round-trip on a one-line mistake.

### `/review-iteration` slash command (already added in iter 0)

The four review subagents fire BEFORE pushing, not in CI. A 30-second token spend prevents 12 minutes of CI failure on a missing `withCostCap` / `politefetch` / idempotency wrap. This is the single highest-leverage CI cost saver in the project — call it out explicitly in the PR description (the PR template's "Reviews fired" section enforces this).

## Acceptance for iteration 0.B as a whole

- [ ] Doc-only PR (`README.md` change) goes green in < 3 minutes; produces no `deploy` / `e2e` / `dast` checks.
- [ ] Frontend-only PR doesn't run the Go lint/test/build matrix.
- [ ] Lambda-only PR doesn't run the frontend build, ZAP, or Lighthouse.
- [ ] Terraform-only PR runs `iac-scan` + `deploy` + smoke; doesn't run frontend or Lambda builds (deploy reuses last successful artifacts via the existing skip-on-cache logic).
- [ ] Cumulative pipeline wall-clock on a typical mixed PR (Lambda + small frontend tweak) drops by ≥ 4 minutes vs the unmodified skeleton.
- [ ] `terraform init` finishes in < 5 s on warm cache.
- [ ] Closing a PR triggers `destroy.yml` only.
- [ ] No skeleton pitfall mitigation regressed (the `skeleton-pitfall-reviewer` agent passes on the iter-0.B diff).

## Watch list (post-iteration)

If we add many new resources to per-env `terraform/` (Cognito, EventBridge, SES config set, Cloudflare R2, Workers KV, KMS), per-PR cold apply may creep past 10 minutes. If/when it does:

- Promote stable resources to `aws-setup/` singletons with per-env namespacing (e.g., one EventBridge bus with per-env prefix on rule names).
- Trades isolation for speed; only do this if the wall-clock cost shows up.
- The skeleton's pitfall #20 (per-branch CloudFront avoidance) is the canonical pattern; follow it for any new resource type that crosses the 1-minute provisioning threshold.
