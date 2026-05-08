# Fix Plan — Website Agency Pipeline

The pipeline is built in 12 iterations (0–11). Each iteration must end with a working demo. Don't jump ahead. Read `.ralph/specs/09-iterations.md` for full acceptance criteria per iteration.

The implementation repo is created from the GitHub template `reaandrew/cloud-skeleton` and customized via `bin/init.sh`. **Iteration 0 is the bring-up of that skeleton plus our agency-specific extensions.**

Marking convention: `[ ]` not done, `[~]` in progress, `[x]` done.

---

## High Priority — Iteration 0.A: Bring up cloud-skeleton

- [x] **0.A.1** Create implementation repo from template `reaandrew/cloud-skeleton`. Clone locally. *(done 2026-05-08: `reaandrew/ai-website-agency`, cloned to `/home/parallels/Development/ai-website-agency`.)*
- [x] **0.A.2** Run `bin/init.sh --dry-run` to preview, then run for real with the project flags. Commit. Delete `bin/init.sh`. *(done 2026-05-08: `--project ai-website-agency --account-id 276447169330 --base-domain agency.techar.ch --parent-domain techar.ch --github-org reaandrew --github-repo ai-website-agency --aws-vault-profile personal_iphone`. Renamed `lambda-edge/finance-*.js` and `cloudfront-functions/finance-cookie-to-auth.js` since `init.sh` substitutes contents but not filenames. `bin/init.sh` deleted.)*
- [x] **0.A.3** Copy `.ralph/`, `.claude/`, `.github/pull_request_template.md`, and `CLAUDE.md` from the spec repo into the implementation repo. Commit. *(done 2026-05-08.)*

> **RALPH: STOP HERE.** Items 0.A.4–0.A.6 are a one-time human ceremony — they require iPhone-MFA-gated `aws-vault exec` and editing NS records on the parent zone. Do **not** attempt `aws-vault`, `terraform apply`, or DNS edits autonomously. When you reach this point in the plan, emit `STATUS: BLOCKED` with `RECOMMENDATION` pointing the operator at `docs/BOOTSTRAP.md` and stop the loop. After the operator completes 0.A.4–0.A.6 and ticks the boxes, you'll resume cleanly at 0.A.7.

- [ ] **0.A.4** **HUMAN ONLY.** AWS bootstrap per `docs/BOOTSTRAP.md`: state bucket, first apply, NS records, second apply, SSM contract, GH secrets. Run with `aws-vault exec personal_iphone -- terraform …` from `aws-setup/`.
- [ ] **0.A.5** **HUMAN ONLY.** First production deploy via `git push origin main`; verify all three URLs respond.
- [ ] **0.A.6** **HUMAN ONLY.** Verify per-PR env: open + close a no-op PR; cleanup must succeed.
- [ ] **0.A.7** Create stub `CLAUDE.md` files in `lambdas/`, `terraform/`, `worker/`, `frontend/` per `11-agents.md` § "Sub-directory CLAUDE.md files". Each is short and references the spec.

## High Priority — Iteration 0.B: CI Efficiency

Apply the changes in `.ralph/specs/12-ci-efficiency.md` to `.github/workflows/deploy.yml` (and `security.yml`) before adding any agency-specific resources to `terraform/`. Doing this first means every subsequent iteration's PR pipeline is fast.

- [ ] **0.B.1** Add `pre-flight` job to `deploy.yml` emitting `frontend_changed`, `lambdas_changed`, `terraform_changed`, `cloudflare_changed`, `worker_changed`, `code_changed`, `run` (via `dorny/paths-filter@v3`).
- [ ] **0.B.2** Gate `deploy`, `smoke-tests`, `post-deploy-seed`, `e2e-tests`, `dast-zap`, `accessibility-lighthouse`, `load-test-k6` on `code_changed == 'true'` (doc-only PR fast lane).
- [ ] **0.B.3** Path-filter the heavy jobs per the table in `12-ci-efficiency.md` § Win 2.
- [ ] **0.B.4** Move `sca` to `security.yml` (weekly cron + on `**/go.mod`/`**/package*.json` changes + manual). Keep `iac-scan` in `deploy.yml` but gated on `terraform_changed`.
- [ ] **0.B.5** Merge `lint-go` + `test-go-unit` into one `go-quality` job. Merge `lint-frontend` + `test-frontend-unit` into one `frontend-quality` job.
- [ ] **0.B.6** Cache Terraform providers via `TF_PLUGIN_CACHE_DIR` + `actions/cache@v4` keyed on `**/.terraform.lock.hcl` in every job that runs `terraform init`.
- [ ] **0.B.7** Drop `closed` from `deploy.yml`'s `pull_request: types`. Remove now-redundant `action != 'closed'` clauses from per-job `if:`.
- [ ] **0.B.8** Shallow checkout (`fetch-depth: 1`) for `secret-scan` on PRs (uses `base`/`head` SHAs).
- [ ] **0.B.9** Centralize `if:` logic into the `pre-flight` job's `outputs.run` and have downstream jobs reference `needs.pre-flight.outputs.run == 'true'`.
- [ ] **0.B.10** Add top-level `Makefile` `ci-local` target replicating the gating jobs locally.
- [ ] **0.B.11** Verify all acceptance criteria in `12-ci-efficiency.md` § "Acceptance for iteration 0.B as a whole" by opening test PRs (doc-only, frontend-only, lambda-only, terraform-only, mixed) and recording wall-clock vs the unmodified skeleton baseline.

## High Priority — Iteration 0.C: Agency-specific Terraform

- [ ] **0.C.1** Extend `terraform/dynamodb-items.tf` with `gsi1`, `gsi2`, `gsi3` per `02-data-model.md`.
- [ ] **0.C.2** `terraform/eventbridge.tf`: custom bus `pipeline${local.env_suffix}` + archive + scheduler group + hourly discovery rule (disabled) + weekly tuner rule (disabled).
- [ ] **0.C.3** `terraform/cognito.tf`: user pool, hosted UI, app client, JWT authorizer on the API. Seed one operator with out-of-band password.
- [ ] **0.C.4** `terraform/ses.tf`: configuration set, suppression list, bounce/complaint SNS topic. Sender `outreach.<base>`.
- [ ] **0.C.5** `terraform/bedrock-iam.tf`: ONE grouped `bedrock-invoke` policy on the two model ARNs (skeleton pitfall #6).
- [ ] **0.C.6** `terraform/s3-blobs.tf`: `pipeline-blobs${local.env_suffix}` bucket; `force_destroy=true` non-prod.
- [ ] **0.C.7** `terraform/sqs.tf`: one DLQ per planned consumer; 14-day retention.
- [ ] **0.C.8** `terraform/kms.tf`: KMS CMK `passcode-cleartext-${env}` for KMS-encrypting passcode cleartext (used by publisher + email-draft). Auto-rotation enabled.

## High Priority — Iteration 0.D: Cloudflare R2 + Worker (passcode-gated)

- [ ] **0.D.1** New `cloudflare/` Terraform stack: R2 bucket `previews${local.env_suffix}` (90d lifecycle on `keep!=true`); Workers KV namespace `PREVIEW_PASSCODES_${env}`; Workers Rate Limiting binding; route `previews.<base>/*` → Worker.
- [ ] **0.D.2** New `worker/` (TypeScript): R2 binding, KV binding, Rate Limit binding, `PASSCODE_SALT` secret. Routes:
  - `GET /sites/{websiteId}` and asset paths under it: cookie or `?p=<passcode>` validation; passcode form on miss.
  - `POST /sites/{websiteId}` (form submit): passcode form handler.
  - `GET /screenshots/{websiteId}/{size}.png`: same passcode rule.
  - `GET /healthz` returns 200.
  - Constant-time argon2id compare (WASM); HMAC-SHA256 signed cookie scoped to `/sites/<websiteId>/`.
- [ ] **0.D.3** `.github/workflows/cloudflare.yml`: deploy `worker/` and apply `cloudflare/` Terraform on changes. Secrets: `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`.

## High Priority — Iteration 0.E: `lambdas/pkg/` extensions

- [ ] **0.E.1** `pkg/events/`: envelope, publisher, consumer wrapper.
- [ ] **0.E.2** `pkg/idempotency/`: `WithIdempotency[T]` per `stdlib/idempotency-patterns.md`.
- [ ] **0.E.3** `pkg/cost/`: `Record`, `Get`, `Assert`, `WithCostCap`.
- [ ] **0.E.4** `pkg/bedrock/`: `InvokeStructured[T]` per `stdlib/json-output-conventions.md`.
- [ ] **0.E.5** `pkg/politefetch/`: robots-aware HTTP client.
- [ ] **0.E.6** `pkg/schemas/`: JSON-Schema generation from Go structs.
- [ ] **0.E.7** `pkg/prompts/`: versioned prompt templates from `07-bedrock-prompts.md`.
- [ ] **0.E.8** `pkg/passcode/`: 8-char Crockford-Base32 generator, argon2id hasher, KMS encrypt/decrypt helpers, KV writer (Cloudflare API).
- [ ] **0.E.9** `pkg/killswitch/`: `Allowed(ctx, stage)` checks `pipelineEnabled` + per-stage flag from `PipelineSettings` (cached 60s).

## High Priority — Iteration 0.F: Pipeline controls

- [ ] **0.F.1** Seed `PipelineSettings` singleton via `aws_dynamodb_table_item` with `lifecycle.ignore_changes=[item]`. Defaults per `05-capacity-and-cost.md`.
- [ ] **0.F.2** `lambdas/api-settings/`: `GET /settings`, `PATCH /settings`. Operator-only via Cognito group claim.
- [ ] **0.F.3** Kill switch enforcement at every (currently-empty) consumer entry.
- [ ] **0.F.4** Cost ledger items + daily rollover Lambda at 00:05 UTC.

## High Priority — Iteration 0.G: Admin shell

- [ ] **0.G.1** Add `react-router-dom`. Routes: `/`, `/queue`, `/settings`, `/login`. Replace `pages/Hello.tsx` with `pages/Dashboard.tsx`.
- [ ] **0.G.2** Auth guard reading runtime-config + cookie presence; redirect to Cognito Hosted UI URL.
- [ ] **0.G.3** Settings page reads/writes `PipelineSettings` via the BFF; master kill switch + per-stage toggles + cap sliders with cost preview.
- [ ] **0.G.4** Bring across the access-strip skeleton component (used in iter 5+) — empty state for now.

## High Priority — Iteration 1: Targeting Profile + Discovery

- [ ] **1.1** `TargetingProfile` model + CRUD API (`lambdas/api-targeting/`) + UI at `/settings/targeting`.
- [ ] **1.2** Discovery providers in `lambdas/discover/providers/`: `companieshouse`, `googleplaces`, `csv`. Each returns normalized `DiscoveredBusiness`.
- [ ] **1.3** `lambdas/discover/` triggered by EventBridge Scheduler hourly rule (enabled here). Idempotent on lowercased domain. Publishes `business.found`.
- [ ] **1.4** `/metrics` discoveries widget; "Run discovery now" button.

## High Priority — Iteration 2: Audit Engine

- [ ] **2.1** Cheap technical pre-audit (`lambdas/pkg/audit/technical/`): PageSpeed API, HTML heuristics. No Bedrock.
- [ ] **2.2** Bedrock qualitative audit (Haiku 4.5) gated on technical threshold; cached on `(domain, html_hash)` TTL 30d via `expires_at`.
- [ ] **2.3** `lambdas/audit/` consumer; stores `Audit`; publishes `website.audit.completed`. DLQ + 3 retries.

## Medium Priority — Iteration 3: Qualification + Backlog

- [ ] **3.1** `priorityScore` pure function with golden tests.
- [ ] **3.2** `lambdas/qualifier/` consumer.
- [ ] **3.3** Backlog logic + `backlog-promoter` Lambda triggered by `queue.slot.freed`.

## Medium Priority — Iteration 4: Spec Generator

- [ ] **4.1** `VerticalStyleGuide` model + seed values.
- [ ] **4.2** `lambdas/spec-generator/` (Sonnet 4.6) with tool-use schema.
- [ ] **4.3** Spec review UI + Approve / Edit / Reject capture path emitting `feedback.captured`.

## Medium Priority — Iteration 5: Component Site Generator + R2 Publish + Passcode

- [ ] **5.1** Component renderers in `lambdas/pkg/components/` (Go templates).
- [ ] **5.2** `lambdas/generator/` consumes Spec → static HTML bundle. Lighthouse ≥ 90 in CI.
- [ ] **5.3** `lambdas/publisher/`: upload to R2; **issue passcode** (8-char Crockford-Base32); write hash to DynamoDB and Workers KV; KMS-encrypt cleartext into `Website.passcodeCipher`; set `passcodeRevealableUntil = now + 7d`. Publishes `website.published` (no cleartext).
- [ ] **5.4** Worker (already provisioned in 0.C) extended to enforce passcode flow with rate-limiting; signed cookie; constant-time compare; revocation propagation < 60s.
- [ ] **5.5** Screenshot job via Cloudflare Browser Rendering API with operator-mode bypass token.
- [ ] **5.6** Site preview Approve/Reject/Regenerate captures `feedback.captured`. Regenerate-site issues a fresh passcode (revoke old).

## Medium Priority — Iteration 6: Review Queue

- [ ] **6.1** `lambdas/api-queue/` (`GET /queue`, paginated by priority).
- [ ] **6.2** Queue UI with thumbnails, filters, action bar, daily-cap awareness.
- [ ] **6.3** Access strip on `/queue/[id]`: copy URL, copy code, show/hide, regenerate code, cleartext-window countdown.

## Medium Priority — Iteration 7: Email Draft Generator

- [ ] **7.1** `EmailToneProfile` model + seed values.
- [ ] **7.2** `lambdas/email-draft/` (Haiku 4.5). KMS-decrypts `passcodeCipher`; calls Bedrock with cleartext via prompt; cache key uses hash; `{{PASSCODE}}` placeholder substitution. Post-validator requires both URL and passcode literals.
- [ ] **7.3** Email review UI; capture path with passcode redacted in `originalPayload`.

## Medium Priority — Iteration 8.5: Reply Triage Agent (production)

- [ ] **8.5.1** `lambdas/reply-triage/` triggered by S3 PUT on the SES inbound bucket. Calls Bedrock Haiku 4.5 via `pkg/bedrock` with the `replyTriage.v1` prompt+schema (added to `07-bedrock-prompts.md` if not yet there).
- [ ] **8.5.2** Routing: `unsubscribe` (≥0.8 conf) → suppression + `Business.status='rejected_after_review'`; `positive_interest` → `Business.status='responded'`; `unknown` (<0.6 conf) → operator inbox at `/replies`.
- [ ] **8.5.3** `/replies` admin page with filter by category, manual reclassify action.

## Medium Priority — Iteration 8: SES Sending + Tracking + Passcode Cleanup

- [ ] **8.1** SES configuration set wired in iteration 0.B; verify domain verification status check.
- [ ] **8.2** `lambdas/sender/` with `MessageDeduplicationId = sha256(contactId+websiteId)`; suppression check before send.
- [ ] **8.3** Bounce/complaint webhook receiver via SNS → Lambda; updates suppression.
- [ ] **8.4** Reply detection via SES inbound rule + S3 + Lambda → marks `Business.status='responded'`.
- [ ] **8.5** Passcode cleanup Lambda: 24h after `email.sent`, zap `Website.passcodeCipher`; emit `preview.passcode.cleartext_wiped`.
- [ ] **8.6** Passcode TTL sweep daily: zap `passcodeCipher` past `passcodeRevealableUntil`.

## High Priority — Iteration 9: Feedback Capture & Learning Loop

- [ ] **9.1** `feedback.captured` events from every Approve/Edit/Reject (carries from iters 4/5/6/7); passcode-redacted in body.
- [ ] **9.2** `/feedback` log UI.
- [ ] **9.3** Weekly tuner Lambdas: `tuner-targeting`, `tuner-style`, `tuner-email-tone` (EventBridge Scheduler Sunday 02:00 UTC).
- [ ] **9.4** `*Delta` items + `/tuners` UI (apply / dismiss / diff). Operator-only; no auto-apply at MVP.

## Low Priority — Iteration 10: Step Functions Orchestration

- [ ] **10.1** `WebsiteGenerationStateMachine` (Express).
- [ ] **10.2** `OutreachStateMachine` (Standard).
- [ ] **10.3** Migrate iter 4–8 producers to invoke state machines.

## Low Priority — Iteration 11: Analytics & Funnel

- [ ] **11.1** Funnel metrics roll-up Lambda.
- [ ] **11.2** Cost dashboard.
- [ ] **11.3** Per-vertical comparison + profile-version splits.

## Completed
- [x] Specifications written (`.ralph/specs/`)
- [x] Repo `reaandrew/ai-website-agency-spec` (spec) created and pushed
- [x] Repo `reaandrew/cloud-skeleton` made a GitHub template

## Notes
- Iteration 0 is split into A/B/C/D/E/F because it's a substantial bring-up. They can be parallelized within reason but A.1–A.6 must complete before B/C/D/E/F start.
- Every iteration must end with a working demo. The skeleton's per-PR ephemeral env is the demo surface.
- Read `10-quality-rules.md` before generating any AI content. Violating those rules is a hard fail.
- Cost discipline: see `05-capacity-and-cost.md`. Total target spend at moderate caps is £60–£100/month at MVP load.
- Passcode cleartext is a secret. Never log, never event, never DDB-unencrypted. See iteration 5 + 7 + 8 specifics.
- Skeleton pitfall mitigations are non-negotiable. See `01-architecture.md` § Skeleton-honored constraints.
