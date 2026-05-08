# 09 — Iteration Plan & Acceptance Criteria

12 iterations (0–11). Each ends with a working demo. Order matters; do not jump ahead.

Acceptance criteria are checkable, observable, and binary. If a criterion is fuzzy, it isn't a criterion — rewrite it.

---

## Iteration 0 — Foundation: cloud-skeleton + agency extensions

**Goal**: The implementation repo exists, customized from `reaandrew/cloud-skeleton`, with the agency-specific terraform (EventBridge, Cognito, SES, Bedrock IAM, DynamoDB GSIs) and the Cloudflare R2+Worker layer added. Pipeline does no work yet, but the controls (kill switch, settings, cost ledger, admin shell) are wired and a per-PR preview env spawns successfully.

**Builds**:

### 0.A — Bring up the skeleton (skeleton's own runbook)
- [ ] **0.A.1 Create implementation repo from template** — On GitHub, click "Use this template" on `reaandrew/cloud-skeleton`; create `<project>` (private). Clone locally.
- [ ] **0.A.2 Customize via `bin/init.sh`** — Run with `--project agency --account-id <id> --base-domain <e.g. agency.example.com> --parent-domain <e.g. example.com> --github-org reaandrew --github-repo agency --aws-vault-profile <profile>`. Verify `--dry-run` first; commit the rewritten files. Delete `bin/init.sh` after.
- [ ] **0.A.3 Bring `.ralph/`, `.claude/`, `.github/pull_request_template.md`, and `CLAUDE.md` across** — Copy from the spec repo (`reaandrew/ai-ai-website-agency`) into the implementation repo at the same paths. Commit.
- [ ] **0.A.4 AWS bootstrap** — Follow `docs/BOOTSTRAP.md`: create the Terraform state bucket out-of-band; first `aws-setup/` apply (will hang at cert validation — expected); add the four NS records to the parent zone; second apply succeeds; verify SSM contract; set `AWS_ROLE_ARN`, `E2E_TEST_USER`, `E2E_TEST_PASS` GitHub secrets.
- [ ] **0.A.5 First production deploy** — `git push origin main`; CI runs full pipeline; verify `https://<base>/`, `https://api.<base>/health`, `https://bff.<base>/health` all return.
- [ ] **0.A.6 Verify per-PR env works** — Open a no-op PR; preview env spawns; visit `https://preview.<base>/<env>/`, `https://api-<env>.<base>/health`, `https://<env>.bff.<base>/health`. Close PR; cleanup workflow destroys everything within 10 minutes.
- [ ] **0.A.7 Sub-directory `CLAUDE.md` stubs** — Create `lambdas/CLAUDE.md`, `terraform/CLAUDE.md`, `worker/CLAUDE.md`, `frontend/CLAUDE.md` per `11-agents.md` § "Sub-directory CLAUDE.md files".

### 0.B — CI Efficiency

Before adding any agency-specific resources, tune the skeleton's pipeline so subsequent iterations land fast. See `12-ci-efficiency.md` for full rationale and per-win acceptance.

- [ ] **0.B.1 Pre-flight job** — Add `pre-flight` to `deploy.yml` emitting `frontend_changed`, `lambdas_changed`, `terraform_changed`, `cloudflare_changed`, `worker_changed`, `code_changed`, `run` outputs (uses `dorny/paths-filter@v3`).
- [ ] **0.B.2 Doc-only PR fast lane** — Gate `deploy`, `smoke-tests`, `post-deploy-seed`, `e2e-tests`, `dast-zap`, `accessibility-lighthouse`, `load-test-k6` on `code_changed == 'true'`.
- [ ] **0.B.3 Path-filter the heavy jobs** — per `12-ci-efficiency.md` § Win 2 table.
- [ ] **0.B.4 Move advisory `sca` to `security.yml`** — weekly cron + on `**/go.mod` / `**/package*.json` changes + manual. Keep `iac-scan` in `deploy.yml` but path-gated on `terraform_changed`.
- [ ] **0.B.5 Merge same-setup jobs** — `lint-go` + `test-go-unit` → `go-quality`; `lint-frontend` + `test-frontend-unit` → `frontend-quality`.
- [ ] **0.B.6 Cache Terraform providers** — `TF_PLUGIN_CACHE_DIR` + `actions/cache@v4` keyed on `**/.terraform.lock.hcl` in every job that runs `terraform init`.
- [ ] **0.B.7 Drop `closed` from PR trigger** — `pull_request: types: [opened, synchronize, reopened]`. Remove now-redundant `action != 'closed'` `if:` clauses.
- [ ] **0.B.8 Shallow secret-scan** — `fetch-depth: 1` on `pull_request`; trufflehog with explicit `base`/`head` SHAs.
- [ ] **0.B.9 Centralize `if:` logic** — downstream jobs read `needs.pre-flight.outputs.run` instead of repeating the long expression.
- [ ] **0.B.10 `make ci-local`** — top-level Makefile target replicating the gating jobs locally.
- [ ] **0.B.11 Validate** — open test PRs (doc-only, frontend-only, lambda-only, terraform-only, mixed) and verify the acceptance table in `12-ci-efficiency.md`.

### 0.C — Agency-specific terraform
- [ ] **0.C.1 Add DynamoDB GSIs** — Extend `terraform/dynamodb-items.tf` with `gsi1`, `gsi2`, `gsi3` per `02-data-model.md`. Migration is non-breaking (sparse GSIs, no rebuild needed for empty table).
- [ ] **0.C.2 Add EventBridge bus + Scheduler** — `terraform/eventbridge.tf`: custom bus `pipeline${local.env_suffix}`, archive (30d dev/staging, 90d prod), and scheduler group. Hourly schedule rule for discovery (initially disabled); weekly schedule rule for tuners (Sunday 02:00 UTC, initially disabled).
- [ ] **0.C.3 Add Cognito** — `terraform/cognito.tf`: user pool, hosted UI domain (`auth${local.env_suffix}.<base>`), app client, JWT authorizer attached to the existing API Gateway. Seed one operator user via Terraform (initial password set out-of-band via SSM with `lifecycle.ignore_changes=[value]` per skeleton pitfall #4).
- [ ] **0.C.4 Add SES configuration set** — `terraform/ses.tf`: configuration set, suppression list, SNS topic for bounces/complaints. Domain verification covered by manual ops; spec the records in `docs/SES.md`. Sender `outreach.<base>` (sub-subdomain doesn't conflict with skeleton's `bff.<base>`).
- [ ] **0.C.5 Add Bedrock IAM** — `terraform/bedrock-iam.tf`: ONE grouped `bedrock-invoke` policy with `bedrock:InvokeModel` action on the two model ARNs (Haiku 4.5, Sonnet 4.6). Attach to the Lambda execution role from `iam.tf`. Never per-call policies (skeleton pitfall #6).
- [ ] **0.C.6 Add `pipeline-blobs` S3 bucket** — `terraform/s3-blobs.tf`: per-env bucket for HTML snapshots and audit raw. `force_destroy = true` for non-prod (skeleton pitfall #1).
- [ ] **0.C.7 Add SQS DLQs** — `terraform/sqs.tf`: one DLQ per planned consumer (audit, qualifier, spec, generator, publisher, email-draft, sender, feedback) with `MessageRetentionPeriod = 14 days`.
- [ ] **0.C.8 Add KMS CMK** — `terraform/kms.tf`: KMS CMK `passcode-cleartext-${env}` for encrypting passcode cleartext (used by publisher + email-draft + cleanup). Auto-rotation enabled.

### 0.D — Cloudflare R2 + Worker (the only Cloudflare bit)
- [ ] **0.D.1 New `cloudflare/` Terraform stack** — Cloudflare provider; R2 bucket `previews${local.env_suffix}` with 90d lifecycle on `keep!=true` tag; Workers KV namespace `PREVIEW_PASSCODES_${env}`; Workers Rate Limiting binding; route `previews.<base>/*` to a Worker. Backend in the same S3 state bucket but at `cloudflare/<env>/terraform.tfstate`.
- [ ] **0.D.2 New `worker/` package** — TypeScript Worker with R2, KV, Rate Limiting bindings + `PASSCODE_SALT` secret. Passcode-gated routes per `09-iterations.md` § Iteration 5.
- [ ] **0.D.3 Wrangler deploy via CI** — Add `.github/workflows/cloudflare.yml` triggered on `cloudflare/**` and `worker/**` changes. `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` as GH secrets.

### 0.E — `lambdas/pkg/` extensions
- [ ] **0.E.1 `pkg/events/`** — Envelope type, publisher (writes to bus and emits via SDK), consumer wrapper that extracts `eventId` and validates envelope version.
- [ ] **0.E.2 `pkg/idempotency/`** — `WithIdempotency(ctx, eventID, fn)`; see `stdlib/idempotency-patterns.md`.
- [ ] **0.E.3 `pkg/cost/`** — `RecordSpend(stage, usd)`, `GetSpend(date, stage)`, `GetCap(stage)`, `WithCostCap(stage, estimate, fn)`. See `05-capacity-and-cost.md`.
- [ ] **0.E.4 `pkg/bedrock/`** — Cost-aware Bedrock client. `InvokeStructured[T any]` per `stdlib/json-output-conventions.md`. Constructs `BedrockRuntimeClient` once.
- [ ] **0.E.5 `pkg/politefetch/`** — robots-aware HTTP client. See `06-discovery-and-compliance.md`.
- [ ] **0.E.6 `pkg/schemas/`** — JSON-Schema generation from Go structs (use `github.com/invopop/jsonschema`). `Validate[T](data)` round-trips through the same struct.
- [ ] **0.E.7 `pkg/prompts/`** — Versioned prompt templates from `07-bedrock-prompts.md`.
- [ ] **0.E.8 `pkg/passcode/`** — 8-char Crockford-Base32 generator, argon2id hasher, KMS encrypt/decrypt helpers, KV writer (Cloudflare API).
- [ ] **0.E.9 `pkg/killswitch/`** — `Allowed(ctx, stage)` checks `pipelineEnabled` + per-stage flag from `PipelineSettings` (cached 60s).

### 0.F — Pipeline controls
- [ ] **0.F.1 `PipelineSettings` singleton** — Seeded in `terraform/dynamodb-items.tf` via `aws_dynamodb_table_item` with `lifecycle.ignore_changes=[item]` so subsequent operator edits aren't reverted. Default values per `05-capacity-and-cost.md`.
- [ ] **0.F.2 `lambdas/api-settings/`** — `GET /settings`, `PATCH /settings` (operator-only via Cognito group). Read-cache 60s in handler-warm container.
- [ ] **0.F.3 Kill switch enforcement** — `pkg/killswitch.Allowed(ctx, stage)` used by every (currently-empty) consumer entry; returns success without acting if disabled.
- [ ] **0.F.4 Cost ledger** — `pk=COST#YYYY-MM-DD` items per stage. Daily rollover Lambda at 00:05 UTC re-enables stages whose pause reason was `budget`. EventBridge Scheduler trigger.

### 0.G — Admin shell (extends skeleton's `frontend/`)
- [ ] **0.G.1 Routing** — Add `react-router-dom`. Routes: `/` (dashboard placeholder), `/queue` (empty list), `/settings`, `/login` (redirect to Cognito Hosted UI). Replace skeleton's `pages/Hello.tsx` with `pages/Dashboard.tsx`.
- [ ] **0.G.2 Auth guard** — Component reads runtime-config + cookie presence; redirects unauthenticated users to Cognito Hosted UI URL.
- [ ] **0.G.3 Settings page** — Reads/writes `PipelineSettings` via the BFF. Master kill switch toggle. Per-stage flags. Cap sliders with cost preview computed from a static cost table (mirror of `05-capacity-and-cost.md`). Visible on the production env and on every preview env.
- [ ] **0.G.4 Access strip skeleton** — Bring across the access-strip component (used in iter 5+) — empty state for now.

**Acceptance**:
- [ ] `bin/init.sh` ran cleanly; no residual `finance` / `levantar` / `134570442530` strings remain anywhere outside `bin/init.sh` itself.
- [ ] Production env URLs all return: `<base>/`, `bff.<base>/health`, `api.<base>/health`.
- [ ] A no-op PR provisions a preview env and the cleanup destroys it on close (one full cycle observed).
- [ ] `aws ssm get-parameters-by-path --path /<project> --recursive` lists all expected entries from BOOTSTRAP.md plus the new `cognito/`, `eventbridge/`, `cloudflare/` keys.
- [ ] `GET /settings` returns the seeded `PipelineSettings` with `pipelineEnabled=true`; `PATCH /settings { pipelineEnabled: false }` is reflected within 60s.
- [ ] Cloudflare Worker at `previews.<base>/healthz` returns `200 OK`; `previews.<base>/sites/anything` returns `404`.
- [ ] No secrets in repo (gitleaks scan green).
- [ ] All skeleton pitfall mitigations still pass (Checkov + tfsec + bootstrap-placeholder check).
- [ ] CI typecheck + lint + tests green on `main` and on the no-op PR.
- [ ] CI efficiency wins from `12-ci-efficiency.md` § "Acceptance for iteration 0.B as a whole" all pass.

---

## Iteration 1 — Targeting Profile + Discovery

**Goal**: The system can autonomously fetch businesses from Companies House and Google Places into DynamoDB, capped, deduped, scheduled.

**Builds**:
- `TargetingProfile` CRUD (API + UI at `/settings/targeting`).
- `DiscoveryProvider` interface + Companies House + Google Places + CSV implementations.
- `discover` Lambda with capacity gate, cost cap, idempotency on domain.
- EventBridge Scheduler hourly rule, gated on `discoveryEnabled`.
- `business.found` event publication.
- `/metrics` shows last-7-days discoveries by source and vertical.

**Acceptance**:
- [ ] Can create a Targeting Profile from the UI; it appears in DynamoDB with the correct GSI keys.
- [ ] Manual "Run discovery now" on a profile produces ≥ 1 `Business` row from Companies House within 60 seconds.
- [ ] Running discovery twice does not duplicate businesses (dedupe verified by domain check).
- [ ] When `maxDiscoveriesPerDay` is hit, additional runs no-op and emit `pipeline.discovery.skipped_capped`.
- [ ] When `discoveryEnabled=false`, no discovery work happens regardless of scheduler firing.
- [ ] Google Places spend is recorded per call; visible at `/metrics` cost panel.
- [ ] Hourly scheduler fires in dev (verified via CW logs over a 2-hour window).
- [ ] Robots.txt is honored on all outbound fetches (unit test on `politeFetch`).

---

## Iteration 2 — Audit Engine

**Goal**: Every newly-discovered business gets audited. Bedrock is invoked only when the cheap technical pre-audit suggests a candidate.

**Builds**:
- Technical pre-audit (PageSpeed Insights API + HTML heuristics).
- `audit.qualitative.v1` Bedrock prompt with caching.
- `audit` Lambda that consumes `business.found`, runs pre-audit, conditionally runs Bedrock, stores `Audit`, publishes `website.audit.completed`.
- Idempotency on `eventId`.
- DLQ + retry policy.

**Acceptance**:
- [ ] Every new `Business` produces exactly one `Audit` within 5 minutes.
- [ ] Replaying the same `business.found` event produces zero new audits and no duplicate Bedrock calls.
- [ ] Bedrock cache hit ratio measurable; second audit of the same homepage HTML never invokes Bedrock.
- [ ] Audit spend recorded; visible at `/metrics`.
- [ ] An audit on a domain that returns 5xx for 3 retries lands in the DLQ.
- [ ] Bedrock prompt assembly is byte-stable (snapshot test passes).
- [ ] When `auditEnabled=false`, audits skip with `pipeline.audit.skipped_killed`.

---

## Iteration 3 — Qualification + Backlog

**Goal**: Audited businesses are scored, qualified or rejected, and parked in the backlog when the queue is full.

**Builds**:
- `priorityScore` pure function with golden tests.
- `qualifier` Lambda, EventBridge rule on `website.audit.completed`.
- `Qualification` item.
- Backlog state when `awaiting_review` count ≥ `maxReviewQueueSize`.
- `backlog-promoter` Lambda triggered by `queue.slot.freed`.

**Acceptance**:
- [ ] A "good" website (auditScore > 80) is rejected; a "weak" website (auditScore < 50) is qualified.
- [ ] Qualification result stored with `priorityScore` in `[0,1]`.
- [ ] When queue is at cap, new qualified items get `awaitingPromotion=true` and don't trigger preview gen.
- [ ] When an item leaves `awaiting_review`, the highest-priority backlog item gets promoted.
- [ ] Operator can manually override qualification from the candidate detail page.
- [ ] Golden tests pass for `priorityScore` (10 fixtures).

---

## Iteration 4 — Spec Generator

**Goal**: Qualified businesses get a structured spec via Sonnet 4.6, validated, stored, reviewable.

**Builds**:
- `VerticalStyleGuide` model + seed values for "default" + the operator's chosen first vertical.
- `spec.v1` Bedrock prompt (Sonnet) with tool-use schema.
- `spec-generator` Lambda gated on `previewEnabled`.
- Spec review UI in `/queue/[id]`.
- **Capture path**: every Approve / Edit / Reject on a Spec emits `feedback.captured` with original + edited payloads. (No tuner yet — that's iter 9.)

**Acceptance**:
- [ ] A qualified business produces exactly one `Spec` within 2 minutes; status `draft`.
- [ ] Generated spec validates against the JSON Schema; invalid outputs rejected by tool-use validator.
- [ ] Spec contains no testimonials, awards, or prices regardless of prompt input (post-validator strips them — adversarial test passes).
- [ ] Operator can edit any spec field in the UI; save bumps `version` and writes a `Feedback` row.
- [ ] Reject sets `status='rejected'` and writes a `Feedback` row with reason and notes.
- [ ] Approve sets `status='approved'`, fires `spec.approved`.
- [ ] Spec spend recorded; cache hit on identical input verified.
- [ ] When `previewEnabled=false`, spec gen skipped.

---

## Iteration 5 — Component Site Generator + R2 Publish + Passcode-Gated Worker

**Goal**: An approved Spec produces a static preview site reachable via the Worker behind a per-website passcode, with screenshots.

**Builds**:
- Component library in `lambdas/pkg/components/` exposing renderers for Hero, Services, About, Trust, FAQ, CTA, Contact, Footer (Go templates → static HTML).
- `lambdas/generator/` renderer that consumes a Spec, emits HTML + assets to a tmpdir.
- `lambdas/publisher/` that uploads HTML+assets to R2 under `sites/{websiteId}/`, issues a passcode, writes `passcodeHash` to DynamoDB **and** Workers KV via the Cloudflare API, KMS-encrypts the cleartext into `Website.passcodeCipher`, sets `passcodeRevealableUntil = now + 7d`. Publishes `website.published` (NO cleartext).
- Cloudflare Worker (`worker/`) with **R2 binding**, **Workers KV binding** (`PREVIEW_PASSCODES`), **Rate Limiting binding**, and a **secret** (`PASSCODE_SALT`):
  - `GET /sites/{websiteId}` (and asset paths under it):
    - If signed cookie `preview_<websiteId>` valid (HMAC-SHA256 of `<websiteId>:<expiry>` with the Worker secret) → fetch from R2.
    - Else if `?p=<passcode>` query → argon2id-hash (using a WASM port) and constant-time compare against KV. On match, set cookie scoped to `/sites/<websiteId>/`, redirect to URL minus `?p`. On mismatch, increment rate-limit and serve form.
    - Else → serve the passcode entry HTML form (POST returns same flow).
  - 5 attempts per IP per websiteId per 10 minutes; 429 after.
  - `Cache-Control: public, max-age=300, s-maxage=86400` only on R2 content; the form page is `Cache-Control: no-store`.
  - `X-Robots-Tag: noindex, nofollow` always.
  - `/healthz` returns `200 OK`.
- Screenshot job using Cloudflare Browser Rendering API. Worker is invoked with the operator-mode bypass (signed admin cookie or a one-time signed query token) so the screenshot can render.
- `Website` item with `previewUrl`, `screenshots`, `passcodeHash`, `passcodeCipher`, `passcodeRevealableUntil` populated.
- **Capture path**: site preview Approve/Reject/Regenerate emits `feedback.captured` with `subject="website"`.

**Acceptance**:
- [ ] An approved spec produces a `Website` with `status='published'` and a reachable `previewUrl` within 3 minutes.
- [ ] Visiting `previewUrl` directly returns the passcode entry form (HTTP 200, HTML), not the preview content.
- [ ] Visiting `previewUrl?p=<correct-cleartext>` sets the cookie and on next request returns the preview HTML with `X-Robots-Tag: noindex, nofollow`.
- [ ] Visiting with an incorrect passcode returns the form again with an inline error and increments the rate-limit counter; 6th attempt within 10 minutes returns 429.
- [ ] Cleartext passcode is **not** present in any DynamoDB item except `passcodeCipher` (KMS) and (later) `EmailDraft.body`; not in CloudWatch logs; not in EventBridge events; not in X-Ray segments.
- [ ] `passcodeRevokedAt` set → Worker rejects all validations within 60s (KV propagation).
- [ ] Lighthouse score ≥ 90 on the rendered site (automated check in CI for fixture spec).
- [ ] Site has no testimonials regardless of spec content (renderer strips even if spec accidentally contains them).
- [ ] Trust badges that aren't in the source site's text are dropped (renderer enforces).
- [ ] Mobile + desktop screenshots captured and stored in R2; URLs on the `Website` item.
- [ ] Operator can Approve / Reject / Regenerate from `/queue/[id]`; Regenerate-site re-renders without invoking Bedrock and issues a fresh passcode (old code immediately invalidated in KV).
- [ ] Operator can Regenerate-passcode without re-rendering the site.
- [ ] When `previewEnabled=false`, generation skipped.

---

## Iteration 6 — Review Queue

**Goal**: The operator opens the app and sees today's top-N candidates in priority order, with thumbnails and one-click actions.

**Builds**:
- `GET /queue?status=awaiting_review` API (paginated, `gsi1` query).
- Queue UI at `/queue` with thumbnails, filters, action bar.
- Backlog count + "X reviewed of N today".
- Approve / Reject / Regenerate / Snooze actions wired through.

**Acceptance**:
- [ ] Queue page shows up to `maxReviewQueueSize` candidates ordered by `priorityScore` desc.
- [ ] Filters by vertical / location / score range narrow correctly.
- [ ] Approve sets `Business.status='approved'`, fires `outreach.email.requested`.
- [ ] Reject opens reason picker; selection writes `Feedback` and sets `Business.status='rejected_after_review'`.
- [ ] Regenerate prompts for notes, fires `website.regenerate.requested`, returns to `awaiting_review` once new preview is ready.
- [ ] Snooze sets `awaitingReviewUntil` and removes from today's list.
- [ ] Counter "X reviewed of N today" updates within 1 second of each action.

---

## Iteration 7 — Email Draft Generator (with passcode)

**Goal**: Approved candidates get an outreach email draft in the operator's chosen tone, including the preview URL and the passcode required to unlock it.

**Builds**:
- `EmailToneProfile` model + seed values.
- `email.v1` Bedrock prompt (Haiku) with strict post-validation that requires both `previewUrl` and the passcode literal.
- `lambdas/email-draft/` triggered by `outreach.email.requested`. KMS-decrypts `Website.passcodeCipher`, calls Bedrock with the cleartext passed via the prompt (cache key uses the hash, not cleartext), substitutes a `{{PASSCODE}}` placeholder in the cached body if the cache is hit. Gated on `outreachEnabled`.
- Email review UI at `/queue/[id]/email` with passcode highlighting + the static checks.
- **Capture path**: Approve/Edit/Reject on a draft emits `feedback.captured` with `subject="email"`. The captured `originalPayload` redacts the passcode (replaced with `{{PASSCODE}}`) so the feedback log doesn't leak cleartext.

**Acceptance**:
- [ ] Approving a candidate produces an `EmailDraft` within 60 seconds; status `draft`.
- [ ] Draft is ≤ 200 words; rejected by post-validator if not.
- [ ] Draft contains the preview URL exactly once.
- [ ] Draft contains the cleartext passcode exactly once.
- [ ] Draft contains the opt-out line verbatim.
- [ ] Draft contains no phrase from `EmailToneProfile.prohibitedPhrases` and does not contain "password".
- [ ] If `Website.passcodeCipher` is null (already wiped), the email-draft Lambda errors out with a clear "regenerate passcode first" message; no draft is created.
- [ ] Operator can edit and approve; edits captured as Feedback with passcode redacted.
- [ ] CloudWatch log assertions: zero log lines contain the cleartext passcode.
- [ ] When `outreachEnabled=false`, drafting skipped.

---

## Iteration 8 — SES Sending + Tracking

**Goal**: Approved drafts go out via SES; bounces/complaints/opens/replies are tracked; suppression respected.

**Builds**:
- SES domain verification + configuration set + suppression list.
- `sender` Lambda triggered by `email.approved`.
- One-click `List-Unsubscribe` header + working unsubscribe URL.
- Bounce/complaint/delivery webhook receiver via SNS.
- Open tracking pixel.
- Click tracking on the preview URL.
- Reply detection via SES inbound rule + S3 + Lambda.
- Suppression check at send time.

**Acceptance**:
- [ ] Approved draft sends within 30 seconds; `EmailEvent` with `event=sent` written.
- [ ] Suppressed email is never sent (unit test + integration test on a suppressed address).
- [ ] Same `(contactId, websiteId)` cannot be sent twice (idempotency test passes).
- [ ] Bounce notification adds the address to suppression and writes `EmailEvent` `event=bounced`.
- [ ] Complaint notification adds the address to suppression.
- [ ] Open and click events arrive and write `EmailEvent` rows.
- [ ] Reply detection updates `Business.status='responded'` and writes `EmailEvent` `event=replied`.
- [ ] One-click unsubscribe URL works without authentication and adds to suppression.
- [ ] Bounce rate alarm trips when crossing 5% over 24h (verified by metric injection in dev).
- [ ] **Passcode cleanup Lambda** runs 24h after `email.sent` and zaps `Website.passcodeCipher`; emits `preview.passcode.cleartext_wiped`. Reading the candidate after cleanup shows "Code wiped — regenerate to view" in the UI; the link still works for the recipient (KV + hash intact).
- [ ] **Passcode TTL sweep** runs daily and zaps `passcodeCipher` on any Website where `passcodeRevealableUntil` has passed even if no email was sent.

---

## Iteration 9 — Feedback Capture & Learning Loop

**Goal**: Operator overrides become weekly tuner runs that propose reviewable deltas to Targeting / Style / Email Tone profiles.

**Builds**:
- `Feedback` log UI at `/feedback` with filters by stage, vertical, date.
- Three weekly Lambdas: `targeting-tuner`, `style-tuner`, `email-tone-tuner` (EventBridge Scheduler, Sunday 02:00 UTC).
- `tuner.targeting.v1`, `tuner.style.v1`, `tuner.email-tone.v1` Bedrock prompts.
- `*Delta` items in DynamoDB; `tuner.delta.proposed` event.
- `/tuners` UI for review/apply/dismiss.

**Acceptance**:
- [ ] Every Approve / Reject / Edit on Audit / Spec / Website / Email already writes a `Feedback` row (carry-over from iters 4/5/6/7).
- [ ] Manually triggering each tuner produces a `*Delta` item that satisfies its JSON Schema.
- [ ] Apply mutates the live profile, bumps `version`, writes a `Feedback` audit row, emits `profile.updated`.
- [ ] Dismiss writes a Feedback row marking the delta dismissed.
- [ ] Tuner never adds anything that would invent a business fact (adversarial fixtures pass).
- [ ] Reply-rate split by `EmailToneProfile.version` visible at `/metrics`.
- [ ] Approve-rate split by `VerticalStyleGuide.version` visible at `/metrics`.

---

## Iteration 10 — Step Functions Orchestration

**Goal**: Replace the EventBridge fan-out for the multi-step flows with Step Functions for visibility and replay. Optional but recommended once the system is in regular use.

**Builds**:
- `WebsiteGenerationStateMachine` (Express): GenerateSpec → AwaitSpecApproval (callback) → GenerateSite → Publish → Screenshot → MarkReadyForReview.
- `OutreachStateMachine` (Standard): GenerateEmail → AwaitEmailApproval → SendViaSES → MarkSent.
- Migrate iter 4–8 producers to invoke state machines instead of fanning events.

**Acceptance**:
- [ ] Each business journey is one Execution per state machine; visible in the AWS console.
- [ ] An execution that fails at any step lands in the DLQ via the catch handler.
- [ ] Replay of a failed execution resumes from the failed step (idempotency holds).
- [ ] No regression in `09-iterations.md` § 4–8 acceptance criteria.

---

## Iteration 11 — Analytics & Optimization

**Goal**: The operator can see funnel performance, cost trends, and per-vertical comparison.

**Builds**:
- Daily roll-up Lambda that writes `Metric` items.
- Funnel report at `/metrics`.
- Cost dashboard.
- Per-vertical reply rate, conversion rate, profile-version splits.

**Acceptance**:
- [ ] `/metrics` shows funnel from `discovered` to `converted` for any chosen date range.
- [ ] Cost dashboard shows daily/monthly Bedrock + Places + SES spend.
- [ ] Vertical comparison sortable by reply rate.
- [ ] Profile version splits show whether tuner-applied deltas correlate with metric movements.
- [ ] Funnel data refreshes within 5 minutes of an event.

---

## Global done criteria

The product is considered MVP-complete when:
- All 12 iterations meet acceptance criteria.
- 100 businesses audited, 30 qualified, 20 previews generated, 100 emails sent.
- 5%+ positive reply rate.
- 1 paying customer.
- Total monthly spend ≤ £200.
- Tuner-applied deltas have improved at least one of (approve-rate, reply-rate) by ≥ 10% relative.

## Open decisions (track here; resolve before the relevant iteration)

- [ ] **Domain choice** — preview domain (`previews.<domain>`) and outreach domain (`outreach.<domain>`). Default in spec is `example.com`; replace before iter 0 deploy.
- [ ] **Sender identity** — display name and reply address for SES. (Defaulted to `Andrew` and `andrew.rea@andrewreaassociates.com` from session context — confirm before iter 8.)
- [ ] **Cognito user list** — single user vs small team. Affects RBAC; spec assumes single operator.
- [ ] **First Targeting Profile** — operator chose "vertical-agnostic" in setup. Spec assumes `vertical=*` is allowed; if not, iter 1 needs a default.
- [ ] **Country scope** — UK-only at MVP, or include EU/US? Affects compliance copy in `/settings/email`. Spec assumes UK-only at MVP.
- [ ] **Auto-apply tuner deltas** — explicitly NOT in MVP. Reconsider after one month of operation.
