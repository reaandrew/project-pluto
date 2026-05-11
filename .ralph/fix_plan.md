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

- [x] **0.A.4** **HUMAN ONLY.** AWS bootstrap per `docs/BOOTSTRAP.md`: state bucket, first apply, NS records, second apply, SSM contract, GH secrets. Run with `aws-vault exec personal_iphone -- terraform …` from `aws-setup/`. *(done 2026-05-08: confirmed by green Deploy workflow on push-to-main + `https://agency.techar.ch/` returning 200.)*
- [x] **0.A.5** **HUMAN ONLY.** First production deploy via `git push origin main`; verify all three URLs respond. *(done 2026-05-09: Deploy run 25595407615 ✅ on `main` after #5 merge; admin `https://agency.techar.ch/` → 200; API GW + Lambda exist (`/healthz` route is iter-0.E work). Worker is on `*.workers.dev` for now — `previews.agency.techar.ch` route was intentionally dropped in commit `f5f8dff`.)*
- [x] **0.A.6** **HUMAN ONLY.** Verify per-PR env: open + close a no-op PR; cleanup must succeed. *(done 2026-05-08/09: PRs #1–#5 each opened-and-merged with green Deploy + Cloudflare workflows on the `pull_request` trigger.)*
- [x] **0.A.7** Create stub `CLAUDE.md` files in `lambdas/`, `terraform/`, `worker/`, `frontend/` per `11-agents.md` § "Sub-directory CLAUDE.md files". Each is short and references the spec. *(done 2026-05-08; `worker/` directory created as a placeholder for iter 0.D.2.)*

## High Priority — Iteration 0.B: CI Efficiency

Apply the changes in `.ralph/specs/12-ci-efficiency.md` to `.github/workflows/deploy.yml` (and `security.yml`) before adding any agency-specific resources to `terraform/`. Doing this first means every subsequent iteration's PR pipeline is fast.

- [x] **0.B.1** Add `pre-flight` job to `deploy.yml` emitting `frontend_changed`, `lambdas_changed`, `terraform_changed`, `cloudflare_changed`, `worker_changed`, `code_changed`, `run` (via `dorny/paths-filter@v3`).
- [x] **0.B.2** Gate `deploy`, `smoke-tests`, `post-deploy-seed`, `e2e-tests`, `dast-zap`, `accessibility-lighthouse`, `load-test-k6` on `code_changed == 'true'` (doc-only PR fast lane).
- [x] **0.B.3** Path-filter the heavy jobs per the table in `12-ci-efficiency.md` § Win 2.
- [x] **0.B.4** Move `sca` to `security.yml` (weekly cron + on `**/go.mod`/`**/package*.json` changes + manual). Keep `iac-scan` in `deploy.yml` but gated on `terraform_changed`. Also dropped `aquasecurity/tfsec-action@v1.0.3` (deprecated upstream + GitHub-API rate-limit on its anonymous binary download); Trivy in `security.yml` covers IaC scanning.
- [x] **0.B.5** Merge `lint-go` + `test-go-unit` into one `go-quality` job. Merge `lint-frontend` + `test-frontend-unit` into one `frontend-quality` job.
- [x] **0.B.6** Cache Terraform providers via `TF_PLUGIN_CACHE_DIR` + `actions/cache@v4` keyed on `**/.terraform.lock.hcl` in every job that runs `terraform init` (`provision-branch`, `lint-terraform`, `deploy`).
- [x] **0.B.7** Drop `closed` from `deploy.yml`'s `pull_request: types`. Remove now-redundant `action != 'closed'` clauses from per-job `if:`.
- [x] **0.B.8** Shallow checkout (`fetch-depth: 1`) for `secret-scan` on PRs (uses `base`/`head` SHAs).
- [x] **0.B.9** Centralize `if:` logic into the `pre-flight` job's `outputs.run` and have downstream jobs reference `needs.pre-flight.outputs.run == 'true'`.
- [x] **0.B.10** Add top-level `Makefile` `ci-local` target replicating the gating jobs locally.
- [ ] **0.B.11** Verify all acceptance criteria in `12-ci-efficiency.md` § "Acceptance for iteration 0.B as a whole" by opening test PRs (doc-only, frontend-only, lambda-only, terraform-only, mixed) and recording wall-clock vs the unmodified skeleton baseline.
- [x] **0.B.12** Branch fast lane: broaden `deploy.yml` `push` trigger to all branches; gate the heavy chain (deploy / smoke / post-deploy-seed / e2e / dast-zap / lighthouse / k6) on `pull_request` events or `push` to `main` only. Push to a feature branch runs only quality + build (lint, test, scan, compile) — no per-PR ephemeral env until a PR is opened. Drops the obsolete `provision-branch` job (covered by `push: '**'`).

## High Priority — Iteration 0.C: Agency-specific Terraform

- [x] **0.C.1** Extend `terraform/dynamodb-items.tf` with `gsi1`, `gsi2`, `gsi3` per `02-data-model.md`.
- [x] **0.C.2** `terraform/eventbridge.tf`: custom bus `pipeline${local.env_suffix}` + archive + scheduler group + hourly discovery rule (disabled) + weekly tuner rule (disabled). *(also adds the three weekly tuner rules at 02:00 UTC Sunday + the scheduler invoke role.)*
- [x] **0.C.3** `terraform/cognito.tf`: user pool, hosted UI, app client, JWT authorizer on the API. Seed one operator with out-of-band password. *(seeding the first operator is a manual `aws cognito-idp admin-create-user` step — not in code; documented in `docs/BOOTSTRAP.md` once iter 0.C lands.)*
- [x] **0.C.4** `terraform/ses.tf`: configuration set, suppression list, bounce/complaint SNS topic. Sender `outreach.<base>`. *(includes domain identity + DKIM CNAMEs + MAIL FROM domain; SES sandbox-out is a separate manual step before iter 8 sends real email.)*
- [x] **0.C.5** `terraform/bedrock-iam.tf`: ONE grouped `bedrock-invoke` policy on the two model ARNs (skeleton pitfall #6).
- [x] **0.C.6** `terraform/s3-blobs.tf`: `pipeline-blobs${local.env_suffix}` bucket; `force_destroy=true` non-prod.
- [x] **0.C.7** `terraform/sqs.tf`: one DLQ per planned consumer; 14-day retention. *(14 consumers seeded — discover, audit, qualifier, backlog-promoter, spec-generator, generator, publisher, email-draft, sender, reply-triage, ses-feedback, tuner-targeting, tuner-style, tuner-email-tone.)*
- [x] **0.C.8** `terraform/kms.tf`: KMS CMK `passcode-cleartext-${env}` for KMS-encrypting passcode cleartext (used by publisher + email-draft). Auto-rotation enabled.

## High Priority — Iteration 0.D: Cloudflare R2 + Worker (passcode-gated)

- [x] **0.D.1** New `cloudflare/` Terraform stack: R2 bucket `previews${local.env_suffix}` (90d lifecycle on `keep!=true`); Workers KV namespace `PREVIEW_PASSCODES_${env}`; Workers Rate Limiting binding; route `previews.<base>/*` → Worker. *(rate-limit implemented as a `cloudflare_ruleset` rather than a Workers Rate Limiting binding — Cloudflare's zone-level ruleset is cleaner per the spec's "10 req / 60s per IP on POST" intent and the Worker doesn't need to call the binding from code.)*
- [x] **0.D.2** New `worker/` (TypeScript): R2 binding, KV binding, Rate Limit binding, `PASSCODE_SALT` secret. Routes:
  - `GET /sites/{websiteId}` and asset paths under it: cookie or `?p=<passcode>` validation; passcode form on miss. ✅
  - `POST /sites/{websiteId}` (form submit): passcode form handler. ✅
  - `GET /screenshots/{websiteId}/{size}.png`: same passcode rule. ✅
  - `GET /healthz` returns 200. ✅
  - **HMAC-SHA256 signed cookie scoped to `/sites/<websiteId>/` ✅. Constant-time argon2id compare**: scaffolded with **SHA-256-with-salt** in this iter (still constant-time-compared); argon2id WASM swap is iter 5.4 work (publisher in 5.3 and Worker in 5.4 will land the swap together so they don't drift on hash format).
- [x] **0.D.3** `.github/workflows/cloudflare.yml`: deploy `worker/` and apply `cloudflare/` Terraform on changes. Secrets: `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`. *(`worker-quality` job (typecheck + lint + format:check + test); `deploy` job runs Terraform apply then `wrangler deploy` with the per-env script name.)*

> **0.D follow-ups (not blocking 0.D merge)**:
> - `destroy.yml` doesn't tear down the cloudflare/ stack on PR close → per-PR R2 buckets + KV namespaces accumulate. Add a `cloudflare-destroy` step or sibling workflow before iter 5.3 (when real preview content lands).
> - `worker/src/passcode.ts` swap from SHA-256-with-salt to argon2id (WASM). Coordinated with iter 5.3 publisher Lambda (the hash format must match across both writers).

## High Priority — Iteration 0.E: `lambdas/pkg/` extensions

- [x] **0.E.1** `pkg/events/`: envelope, publisher, consumer wrapper. *(done 2026-05-09: `Envelope[T]` generic with eventId auto-gen + `WithCorrelation`/`WithCausation` chainers + `Validate`; `Publisher` with injectable `EventBridgeAPI` and `EVENT_BUS_NAME` env-var bootstrap; `Unmarshal` / `FromEventBridge` / `FromSQS` decoders; `Consume[T]` partial-batch-failure SQS dispatcher. 89.1% coverage.)*
- [x] **0.E.2** `pkg/idempotency/`: `WithIdempotency[T]` per `stdlib/idempotency-patterns.md`. *(done 2026-05-09: `WithIdempotency[T any](ctx, consumer, eventID, fn)` with `IDEMP#<consumer>#<eventID>` PK per the spec's "second pattern" footnote, conditional `attribute_not_exists(pk)` write, `ErrAlreadyProcessed` for replays, 24h TTL via `expires_at`. Required `pkg/ddb` extension: lazy `Client(ctx)` + `SetClient` test hook + `TableName()` reading `ITEMS_TABLE`. Coverage: idempotency 92.0%, ddb 94.7%.)*
- [x] **0.E.3** `pkg/cost/`: `Record`, `Get`, `Assert`, `WithCostCap`. *(done 2026-05-09: daily-spend ledger at `pk=CAP#<YYYY-MM-DD>, sk=STAGE#<stage>` per `05-capacity-and-cost.md`; atomic `UpdateItem ADD spentUsd, callCount`; 30d `expires_at` TTL; `Assert` returns `ErrBudgetCapExceeded` when `spent + estimate > cap`; `WithCostCap[T]` orchestrates Assert→fn→Record with fn returning `(T, actualUsd, err)` so token-billed calls record true cost. Cap is an explicit param — `pkg/killswitch` (0.E.9) will source it from PipelineSettings. Widened `pkg/ddb.API` with `GetItem`/`UpdateItem`. 93.1% coverage.)*
- [x] **0.E.4** `pkg/bedrock/`: `InvokeStructured[T]` per `stdlib/json-output-conventions.md`. *(done 2026-05-09: full pipeline — cache hit → `cost.Assert` → `InvokeModel` with forced `tool_choice` + temp 0.2 default → tool_use extraction (`ErrNoToolUse` if absent) → injectable `Validator` → unmarshal into `T` → `cost.Record(actualUsd)` from token usage → cache-write with 30d TTL. Pricing table in `pricing.go` for Haiku 4.5 + Sonnet 4.6 (per-token, not per-million); `SetPricing` for runtime overrides. `Validator` interface with `nopValidator` default — `pkg/schemas` (0.E.6) wires the real one at startup. 90.2% coverage.)*
- [x] **0.E.5** `pkg/politefetch/`: robots-aware HTTP client. *(done 2026-05-09: `Client.Fetch` does robots.txt allow-check (24h DDB cache + RFC 9309 5xx → disallow / 4xx → allow); fleet-wide per-host token bucket via DynamoDB conditional `UpdateItem` (default 1 req per 5s, honours `Crawl-delay` if stricter); ETag/Last-Modified cache (24h TTL) short-circuiting on 304; 429/5xx exponential backoff with decorrelated jitter (3 retries default); documented bot UA `WebsiteAgencyBot/1.0 (+…/bot)`. Uses temoto/robotstxt for parsing. 85.2% coverage.)*
- [x] **0.E.6** `pkg/schemas/`: JSON-Schema generation from Go structs. *(done 2026-05-09: `JSONSchemaFor[T]() (json.RawMessage, error)` reflects via invopop/jsonschema with `DoNotReference`+`RequiredFromJSONSchemaTags`; strips `$schema`/`$id` so the result is the bare object shape Bedrock tool-use expects. `Validator` validates payloads with santhosh-tekuri/jsonschema/v5, caches compiled schemas; structurally satisfies `bedrock.Validator` so lambdas wire it via `bedrock.SetValidator(schemas.NewValidator())` at startup. 94.6% coverage; integration_test.go pins the structural-typing contract.)*
- [x] **0.E.7** `pkg/prompts/`: versioned prompt templates from `07-bedrock-prompts.md`. *(done 2026-05-09: framework only — `Prompt[T]` struct, `New[T]` constructor (panics on missing fields, eagerly populates Schema via schemas.MustJSONSchemaFor[T]), `Apply` to convert into bedrock.InvokeInput, `HashInputs` deterministic input-hasher with unit-separator (avoids segmentation collisions), `WrapBlock` XML-block helper, `SafetyRulesBlock` constant referencing 10-quality-rules.md. Each prompt's actual file (audit_qualitative_v1.go, spec_v1.go, etc.) lands alongside the consumer Lambda iteration that uses it — that's also when bedrock-prompt-reviewer fires. 100% coverage.)*
- [x] **0.E.8** `pkg/passcode/`: 8-char Crockford-Base32 generator, argon2id hasher, KMS encrypt/decrypt helpers, KV writer (Cloudflare API). *(done 2026-05-09: `Generate()` 8-char Crockford-Base32 (alphabet excludes I/L/O/U) via crypto/rand.Int — no modulo bias; `Hash(passcode, salt)` uses SHA-256 of `<passcode>|<salt>` matching `worker/src/passcode.ts:hashPasscode` (cross-check vector pinned in tests so the format stays in lockstep — argon2id swap is iter 5.4 work, coordinated with publisher 5.3); `ConstantTimeEqual` via `crypto/subtle`; `IsValidPasscodeFormat` for early-rejection in publisher; `EncryptCleartext`/`DecryptCleartext` via KMS SDK with base64-wrapped ciphertext for DDB string storage; `KVWriter` posts hash via Cloudflare bulk-PUT (`/storage/kv/namespaces/<id>/bulk`) with metadata, `Delete` for revocation. argon2id deferred — pulled `golang.org/x/crypto v0.51` would have forced go directive to 1.25 (CI is 1.24.13). 92.6% coverage.)*
- [x] **0.E.9** `pkg/killswitch/`: `Allowed(ctx, stage)` checks `pipelineEnabled` + per-stage flag from `PipelineSettings` (cached 60s). *(done 2026-05-09: `Get(ctx)` reads singleton `pk=SETTINGS#PIPELINE, sk=CURRENT` with `ConsistentRead=true` (operator-toggle latency); 60s in-process cache; `Allowed(ctx, stage)` returns `pipelineEnabled && stages.<stage>Enabled`; `CapUSD(ctx, stage)` maps stage → `Budgets.*` for `pkg/cost.WithCostCap`; `StageMap` documents the consumer-Lambda → operator-stage rollup (publisher → preview, qualifier → audit, etc.); `Defaults()` mirrors `05-capacity-and-cost.md`. `SetSettings`/`SetCacheTTL` test hooks. Missing-row error explicitly references the 0.F.1 seed step. 92.3% coverage.)*

## High Priority — Iteration 0.F: Pipeline controls

- [x] **0.F.1** Seed `PipelineSettings` singleton via `aws_dynamodb_table_item` with `lifecycle.ignore_changes=[item]`. Defaults per `05-capacity-and-cost.md`. *(done 2026-05-09: `terraform/pipeline-settings.tf` creates `pk=SETTINGS#PIPELINE, sk=CURRENT` once with the documented defaults; `lifecycle.ignore_changes=[item]` mirrors the SSM-placeholder pattern (Pitfall #4) so operator edits via /settings persist across `terraform apply`s. Drift guard: `lambdas/pkg/killswitch/seed_test.go` parses the heredoc out of the .tf file, unwraps the DDB-typed JSON, and asserts it equals `Defaults()` — a doc bump that updates one without the other now fails the test.)*
- [x] **0.F.2** `lambdas/api-settings/`: `GET /settings`, `PATCH /settings`. Operator-only via Cognito group claim. *(done 2026-05-09: new `lambdas/pkg/auth/` parses `cognito:groups` from the V2 HTTP API JWT-claims context tolerantly — handles `[operator]`, `["operator"]`, bare `operator`, comma/space variants — and exposes `IsOperator(req)`. `lambdas/api-settings/` routes on `req.RequestContext.HTTP.Method`: GET serves the killswitch-cached row (60s warm-container); PATCH reads fresh from DDB (consistent), validates that body keys ⊆ {pipelineEnabled, stages, caps, thresholds, budgets}, unmarshals each top-level key onto the populated Settings struct (so sub-objects deep-merge field-by-field — a single cap update keeps the rest), PutItem-replaces the row, then `killswitch.SetSettings(&new)` so the same warm container's next consumer-Lambda call sees the change without waiting on the 60s TTL. Both routes are JWT-protected at the API Gateway via `aws_apigatewayv2_authorizer.cognito` and additionally gated to operator-group in the handler. Reuses the `lambda_api` IAM role — DDB read/write was already granted.)*
- [x] **0.F.3** Kill switch enforcement at every (currently-empty) consumer entry. *(done 2026-05-09: `lambdas/pkg/killswitch/wrap.go` adds `WithKillSwitch(ctx, stage, fn func(ctx) error) error` — folds the three required steps into one entry call: pre-flight `Allowed(ctx, stage)`, on disabled emit a structured `pipeline.<stage>.skipped_killed` log line (msg + stage + metric fields, parseable by CloudWatch metric filters) and return nil so the Lambda runtime treats it as a successful no-op, on allowed call `fn(ctx)` and surface its error verbatim. Fails closed on `Get` errors so a transient DDB hiccup retries via DLQ rather than silently bypassing the gate. Unknown stage rejects in tests rather than shipping a fake-gated consumer. There are no consumer Lambdas yet — first usage lands in iter 1.3 (`lambdas/discover/`) and iter 2.3 (`lambdas/audit/`). 7 tests; 100% coverage on the new file.)*
- [x] **0.F.4** Cost ledger items + daily rollover Lambda at 00:05 UTC. *(done 2026-05-09: cost ledger items already shipped in iter 0.E.3 (`pkg/cost`) — this iteration adds the rollover. New `StagePauseReasons` typed struct on `Settings` mirrors `StageFlags` one-for-one with `omitempty` JSON tags; reserved `PauseReasonBudget="budget"` constant the cost-cap mechanism writes when a daily cap is hit and the rollover matches against. `lambdas/cost-rollover/main.go` reads the singleton (consistent), the pure `rollover()` decision function flips Stages.<X>Enabled back to true and clears the pause reason for any stage where Enabled=false AND reason==budget, PutItem persists, then primes the killswitch cache. Operator-disabled stages (no pause reason set) are left untouched — that's the contract that lets operators keep stages off across days. EventBridge Scheduler `cron(5 0 * * ? *)` ENABLED on every env (5-min offset gives DDB metric rollups time past midnight); reuses the existing `scheduler_invoke` IAM role whose wildcard already covers `ai-website-agency-*` Lambdas. 9 tests covering pure-function rollover (no-op, budget-paused single + multi, operator-paused untouched, unknown-reason untouched, mixed sources) + handler integration (no-op, persist, read-error, write-error, cache-prime). Note: the spec at `02-data-model.md` documents cost ledger pk as `COST#YYYY-MM-DD` while `pkg/cost` shipped using `CAP#YYYY-MM-DD`; pre-existing divergence from iter 0.E.3, out of scope here.)*

## High Priority — Iteration 0.G: Admin shell

- [x] **0.G.1** Add `react-router-dom`. Routes: `/`, `/queue`, `/settings`, `/login`. Replace `pages/Hello.tsx` with `pages/Dashboard.tsx`. *(done 2026-05-09: `react-router-dom@^7` added; `frontend/src/App.tsx` is now the routed shell — header + nav strip + `<Outlet/>`; `main.tsx` mounts `<BrowserRouter><Routes><Route path="/" element={<App/>}>` with index/`queue`/`settings`/`login` children. Four stub pages added: `Dashboard.tsx` (BFF /health surface — same content the old Hello.tsx had, ready for KPIs to replace it later), `Queue.tsx` (empty list placeholder for iter 6.1+), `Settings.tsx` (placeholder for iter 0.G.3 form), `Login.tsx` (placeholder; the actual Cognito Hosted UI redirect is iter 0.G.2 since it needs runtime-config plumbing). Hello.tsx deleted. SPA fallback already handled by both CloudFront distributions — production via `custom_error_response`, preview via the existing `preview_router` Lambda@Edge that rewrites extensionless paths to `/<env>/index.html`. Frontend tests: api.test.ts + new App.test.tsx (6 cases) green; @testing-library/react + jest-dom installed; vitest test-setup wired. typecheck clean, lint clean, build clean.)*
- [x] **0.G.2** Auth guard reading runtime-config + cookie presence; redirect to Cognito Hosted UI URL. *(done 2026-05-11: new `frontend/src/AuthGuard.tsx` reads `document.cookie` for an entry named `auth_token` (same name the substrate's cookie-to-auth CFFn looks for) and, when absent, calls `window.location.replace(COGNITO_LOGIN_URL)`. main.tsx splits the route tree: `/login` is public, everything else (Dashboard/Queue/Settings) is wrapped in `<Route element={<AuthGuard/>}>` and renders the protected `<Outlet/>` only on cookie presence. `/login` itself also redirects on mount via the same URL, so it works as the click-through "Sign in" destination. RuntimeConfig gains `cognitoHostedLoginUrl`; empty string disables the redirect for local dev so `npm run dev` doesn't bounce to an unreachable Cognito URL. `scripts/deploy-frontend.sh` builds the URL from terraform outputs (`cognito_hosted_ui_domain`, `cognito_app_client_id`) passed via env vars from the workflow's "Capture endpoints" step. Callback URL hard-mirrors `terraform/cognito.tf`'s `callback_urls` shape. E2E health spec sets the cookie via `context.addCookies` so the test still exercises the BFF /health round-trip. 4 new AuthGuard tests; 12 frontend tests total all green; typecheck/lint/format/build clean.)*
- [x] **0.G.3** Settings page reads/writes `PipelineSettings` via the BFF; master kill switch + per-stage toggles + cap sliders with cost preview. *(done 2026-05-11: `frontend/src/pages/Settings.tsx` GET-on-mount via the new `getSettings()` API client; controlled form for master toggle, four per-stage flags, six daily caps, three budget caps, three thresholds; auto-paused tag rendered next to any stage whose `stagePauseReasons.<stage>` is "budget" (only the cost-rollover Lambda flips this back). Save submits the whole edited object as a PATCH; the api-settings Lambda's deep-merge means a partial-shape submit would also work but the full-shape submit avoids ambiguity. Cost preview is a pure `computeDailyCostUsd(settings)` function exposed for testing — unit costs ported from `.ralph/specs/05-capacity-and-cost.md` § Cost model; total and per-stage figures recompute live as the operator edits caps. 8 new Settings tests + 2 cost-preview tests; 20 frontend tests total all green; typecheck/lint/prettier/build clean.)*
- [x] **0.G.4** Bring across the access-strip skeleton component (used in iter 5+) — empty state for now. *(done 2026-05-11: `frontend/src/components/AccessStrip.tsx` implements the layout documented in `.ralph/specs/08-admin-ui.md` § "Single candidate" — Access URL row with [Copy URL]; Code row with [Copy code] / [Show/Hide] / [Regenerate code]; cleartext-window countdown. Props are all optional so the empty state (no preview yet) renders cleanly. Show/Hide toggles state-local visibility; the four actions are wired to optional callbacks so iter 5.3 (publisher) + 6.3 (queue-detail page) can drop them in. Past-cleartext-window date renders "Code wiped — regenerate to view" and hides Copy code / Show. `formatRemaining()` helper does the "Nd Mh" countdown, frozen-clock tested. Queue.tsx mounts the empty-state component so the layout is visible on the per-PR env without waiting for iter 6. 8 new AccessStrip tests; 28 frontend tests total all green.)*
- [x] **0.G.5** OAuth callback handler — exchange Cognito authorization code for `auth_token` cookie. *(done 2026-05-11: not originally pinned in the spec but required for the auth flow to actually work end-to-end. `frontend/src/auth.ts` does the SPA-side PKCE dance — `beginPkceFlow(loginUrl)` generates a 384-bit random verifier, stashes it in sessionStorage, returns the login URL with the matching S256 `code_challenge`. `frontend/src/pages/Callback.tsx` registered at `/oauth/callback` (public route) reads `?code=` from URL, retrieves the verifier, POSTs to Cognito's `/oauth2/token` endpoint, gets back the id_token, writes it as the non-HttpOnly `auth_token` cookie (Max-Age 30min, Path /, SameSite Lax, Secure on HTTPS), wipes the verifier, navigates to /. Errors render a recoverable "Start again → /login" link. AuthGuard internal-navigates to `/login` (not a hard redirect) so Login.tsx's PKCE prep always runs before the external bounce. RuntimeConfig gains `cognitoAuthOrigin` + `cognitoClientId` + `cognitoRedirectUri`; deploy script emits them. Tradeoff: cookie is non-HttpOnly (XSS-readable) because the SPA sets it from JS; a future iter can move the exchange to a BFF Lambda for HttpOnly. 5 new Callback tests + restructured AuthGuard tests + Login simplified; 34 frontend tests total green.)*

## High Priority — Iteration 1: Targeting Profile + Discovery

- [x] **1.1** `TargetingProfile` model + CRUD API (`lambdas/api-targeting/`) + UI at `/settings/targeting`. *(done 2026-05-11: new shared `lambdas/pkg/targeting/` package owns the `Profile` struct (mirrors `02-data-model.md`), Validate (weights sum to 1.0 ± 0.01, required fields), and the five CRUD helpers (Create/Get/List/Update/Delete). Stats are server-managed: Create zeroes them, Update preserves the persisted values, so an API caller can't fabricate historical performance. Etag is rotated on every write; ConditionExpression on the etag turns into the 412 `ErrEtagMismatch` sentinel on optimistic-concurrency failure. `lambdas/api-targeting/` Lambda routes five paths through the standard operator-auth gate (same Cognito JWT + group-claim check as api-settings); If-Match header carries the etag on PATCH. New `terraform/api-targeting.tf` wires log group + Lambda + integration + the five JWT-authorized routes; reuses lambda_api role (DDB read/write already granted). The shared `ddb.API` interface was widened to include `Scan` + `DeleteItem` (an Explore agent stubbed the nine existing test fakes). Frontend: api.ts gains TargetingProfile types + four CRUD clients; `frontend/src/pages/Targeting.tsx` is a list view with inline editor — keyword comma-split, weight inputs with live sum validation that disables Save when the sum drifts from 1.0, server-rotated etag carried in `If-Match` on PATCH so a stale-write returns 412 → "Save failed" message. Settings page now links to `/settings/targeting`. 8 targeting tests + 12 api-targeting tests + 8 frontend Targeting tests + 4 frontend api/route updates; 42 frontend tests total; Go test suite clean; typecheck/lint/format/build all clean.)*
- [x] **1.2** Discovery providers in `lambdas/discover/providers/`: `companieshouse`, `googleplaces`, `csv`. Each returns normalized `DiscoveredBusiness`. *(done 2026-05-11 across two PRs. 1.2a: shared `lambdas/pkg/discovery/` package (`DiscoveredBusiness`, `Provider` interface, `FindRequest`) + CSV provider (operator-uploaded S3 CSV → DiscoveredBusiness; pure `parseCSV` exposed for testing). 1.2b: CompaniesHouse + GooglePlaces. CompaniesHouse uses HTTP Basic auth with the API key as username + empty password (per CH docs), free; query built from vertical + location + include-keywords, response items mapped with active=0.85 / dissolved=0.6 confidence, domain stays empty (CH carries no websites — audit Lambda resolves later). GooglePlaces uses POST /v1/places:searchText with X-Goog-Api-Key + an X-Goog-FieldMask that narrows the response to (id, displayName, websiteUri, formattedAddress) — keeps the per-call cost down to the documented $0.017. Domain is normalised through `normaliseDomain` (strips scheme + www + trailing path; rejects no-dot strings). Confidence: 0.9 with website, 0.6 without. Both providers reject 401/403 with "API key rejected", surface 429 as "rate-limited — discover Lambda should back off" so the iter 1.3 caller can implement backoff. politefetch deliberately NOT used here — these are authenticated API endpoints with their own rate limits, not scrape-style fetches that need robots.txt enforcement. cost.WithCostCap wrapping happens at the discover Lambda level in 1.3, not in the provider itself. 11 CH tests + 13 Places tests + 11 CSV tests; full Go test sweep + golangci-lint clean.)*
- [x] **1.3** `lambdas/discover/` triggered by EventBridge Scheduler hourly rule (enabled here). Idempotent on lowercased domain. Publishes `business.found`. *(done 2026-05-11: `lambdas/discover/main.go` is the first real consumer Lambda. Handler wraps `killswitch.WithKillSwitch(StageDiscovery)` so pipeline-off → no-op + skipped_killed metric. `run()` is the testable core taking a `runDeps` struct (ListProfiles, Providers, Publish, BudgetUSD, Now, NewBizID); production wires the real impls in `buildDeps()`. For each enabled TargetingProfile × each provider: `cost.WithCostCap` wraps the Places call against `Budgets.DailyPlacesUsd`; on `ErrBudgetCapExceeded` the provider is added to a `skipProvider` set so subsequent profiles don't retry it this run (cost-rollover Lambda re-enables tomorrow). Per-domain dedup via `Query` on `gsi3` (`DOMAIN#<lowercased>`) before any PutItem. `attribute_not_exists(pk)` ConditionExpression on the PutItem handles the race-window where two providers in the same run produce the same domain. Companies-House results without domains are skipped + logged (audit Lambda resolves later, iter 2). Publishes `business.found` envelope via `pkg/events.Publish`; publish failures are logged but the row stays persisted — downstream consumers can recover via the `BUSINESS#STATUS#new` gsi1. Terraform: `terraform/discover.tf` adds Lambda + log group + two SSM parameters for the API keys (placeholders + `lifecycle.ignore_changes=[value]` so operator-supplied keys aren't clobbered on apply). Schedule `aws_scheduler_schedule.discover_hourly` updated from `DISABLED` to `ENABLED` with the real ARN target. The shared `ddb.API` interface was widened with `Query` (11 fake stubs added across the test suite by an agent). 10 discover tests covering happy-path, disabled-profile skip, dedup-known-domain, no-domain skip, lowercase normalisation before dedup, cap-exceeded-skips-rest, provider-failure-isolation, publish-failure-doesn't-rollback-DDB, ListProfiles error surface. Full Go test sweep + golangci-lint + terraform fmt/validate clean.)*
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
