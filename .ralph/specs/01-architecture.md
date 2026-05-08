# 01 — Architecture

## Starting point: `reaandrew/cloud-skeleton` template

The implementation repo is **created from the GitHub template `reaandrew/cloud-skeleton`** ("Use this template" → "Create a new repository") and then customized with `bin/init.sh`. This gives us, on day one:

- AWS account bootstrap pattern (Route53 zone + ACM certs + OIDC role + WAF + CloudFronts) in `aws-setup/` — apply once with `aws-vault`.
- Per-environment Terraform stack (`terraform/`) applied by CI per branch, with a separate tfstate key per env.
- A reference Go Lambda on `provided.al2023` (`lambdas/api-hello/`) and shared `lambdas/pkg/` (config, ddb, httpresp, log).
- API Gateway v2 (HTTP API) with custom domain, CORS, throttling, access logs, X-Ray.
- DynamoDB single-table (`<project>-items[-<env>]`), `pk` + `sk`, `expires_at` TTL, PITR + deletion-protection in prod only.
- Vite 6 / React 19 / TypeScript frontend in `frontend/`, with the load-bearing `base: './'` and runtime-config pattern (`/runtime-config.js` written per env at deploy time).
- BFF CloudFront with cookie→Authorization Header CFFn and Lambda@Edge bff-router for per-branch routing.
- Production CloudFront for `<base-domain>` + Preview CloudFront with Lambda@Edge preview-router for `preview.<base-domain>/<env>/`.
- A GitHub Actions deploy pipeline that on every PR provisions a fully isolated preview env and destroys it on PR close.
- Quality gates: lint + types + unit + secret-scan + SAST (Semgrep) + SCA + IaC (Checkov + tfsec) + Trivy + smoke + E2E (Playwright) + DAST (ZAP) + a11y (Lighthouse) + perf (k6).
- 20 mitigated pitfalls (see `docs/ARCHITECTURE.md` in the impl repo) — do not relitigate them.

We **add on top** of this skeleton: extra DynamoDB GSIs; EventBridge bus + Scheduler; SES + suppression list; Bedrock IAM; Cognito user pool; the Cloudflare provider for R2 + a Worker that hosts the generated business-preview sites; and the agency-specific Lambdas in new `lambdas/<service>/` directories sharing `lambdas/pkg/`.

We never modify the skeleton's mitigations. If a constraint is documented in `docs/ARCHITECTURE.md` § Pitfalls, it is non-negotiable here.

## Stack at a glance

| Concern | Choice | Notes |
|---|---|---|
| IaC | **Terraform 1.9** in two stacks: `aws-setup/` (singletons, `aws-vault` apply) and `terraform/` (per-env, CI apply). Cloudflare resources via the same Terraform with the Cloudflare provider in a small `cloudflare/` stack. | Inherited from skeleton. Singletons NEVER live in `terraform/` (skeleton pitfall #10). |
| Compute | **AWS Lambda — Go 1.24** on `provided.al2023`, x86_64. Each service is `lambdas/<name>/` with `main.go` + `main_test.go` + a placeholder `bootstrap`. Shared code in `lambdas/pkg/`. | Inherited from skeleton. Bootstrap placeholder is committed (skeleton pitfall #3). |
| API | API Gateway HTTP API (v2) with custom domain `api[.|-<env>].<base-domain>`, CORS, throttling. JWT authorizer added by us for Cognito. | Inherited from skeleton; we add JWT auth + the agency routes. |
| Auth | **Cognito user pool + hosted UI** in `terraform/`. JWT used by the Authorizer; the BFF CFFn translates the cookie set by the Cognito Hosted UI into `Authorization: Bearer <jwt>` for API calls. | Cognito is added by us; the cookie-to-Authorization pattern is already wired in the skeleton's BFF CloudFront. |
| Events | **EventBridge** custom bus (`pipeline-{env}`) + **EventBridge Scheduler** for cron triggers (every hour for discovery, weekly for tuners). Added in `terraform/eventbridge.tf`. | Not in skeleton. |
| Queues | **SQS** for retry/DLQ — one DLQ per consumer Lambda. | Not in skeleton; standard. |
| Workflow | **Step Functions** (Express for short, Standard for long) — added in iteration 10 only. | Not needed earlier; EventBridge fan-out is enough. |
| Database | **DynamoDB single-table** `<project>-items[-<env>]`, hash `pk` + range `sk`. We extend the skeleton's table with three GSIs and the `expires_at` TTL attribute (already wired). | Inherited from skeleton. PITR + deletion-protection prod-only (pitfall #2). |
| Object storage | **S3** for internal blobs (HTML snapshots, audit raw, Terraform state). The skeleton already provisions `<project>-frontend-production` + `<project>-frontend-preview-shared-<acct>` for the admin app and per-env upload buckets. We add a `pipeline-blobs[-<env>]` bucket for our internal blobs. | Inherited + extended. |
| **Generated business-preview hosting** | **Cloudflare R2 + a single Cloudflare Worker (with Workers KV + Rate Limiting bindings).** R2 holds `sites/{websiteId}/index.html` + assets. Workers KV holds `passcode:{websiteId}` → argon2id hash. The Worker enforces a per-website passcode before serving any preview asset and sets a signed cookie scoped to that websiteId path on success. **This is the only Cloudflare component.** | Added by us; not in skeleton. |
| Passcode generation + cleartext custody | Publisher Lambda generates an 8-char Crockford-Base32 passcode (no `I/L/O/U`), argon2id-hashes it (salted with a per-env Worker secret), writes the hash to DynamoDB **and** Workers KV, and stores the cleartext **KMS-encrypted** on the Website item with `passcodeRevealableUntil` 7 days out. Cleanup Lambda zaps the cleartext 24h after `email.sent` (or at `passcodeRevealableUntil` if no send). | Added by us. The KMS key is an environment-scoped CMK in `aws-setup/`; only the publisher, email-draft, and operator-read Lambdas have `kms:Decrypt`. |
| AI | **Amazon Bedrock** — Claude Haiku 4.5 + Claude Sonnet 4.6. Region `eu-west-2` (use `bedrock-runtime` cross-region inference where the model isn't in eu-west-2 yet). | IAM role policy added in `terraform/iam.tf` (one grouped policy — pitfall #6). |
| Email | **Amazon SES** with configuration set + suppression list + bounce/complaint topics. Sending domain `outreach.<base-domain>`. | Not in skeleton; added by us. |
| Frontend (admin app) | **Vite 6 / React 19 / TypeScript 5** in `frontend/`. Built once per pipeline run; per-env `runtime-config.js` written next to `index.html` after `aws s3 sync`. | Inherited from skeleton. `vite.config.ts` `base: './'` is load-bearing for path-prefix preview — never change it (pitfall #17). |
| Admin app hosting | **CloudFront + S3** for the admin app — production at `<base-domain>`, preview at `preview.<base-domain>/<env>/`. | Inherited from skeleton. Cloudflare Pages is **not** used for the admin app. |
| Secrets | **AWS Secrets Manager** + `terraform/ssm-parameters.tf` for non-secret config. SSM placeholders for secret values; values set out-of-band; `lifecycle.ignore_changes=[value]` (pitfall #4). | Inherited + extended for Cloudflare API token, Companies House, Google Places. |
| Observability | **CloudWatch Logs** with explicit 30d retention per Lambda log group (pitfall #5); **X-Ray** on every Lambda; **CloudWatch Metrics** + Alarms; structured JSON logs via `lambdas/pkg/log/`. | Inherited from skeleton. |

## High-level diagram

```
                                ┌───────────┐
                                │ Operator  │
                                └─────┬─────┘
                                      ▼
                  ┌───────────────────────────────────────┐
                  │  Admin app (Vite/React 19)            │
                  │  hosted on CloudFront+S3              │
                  │  prod:    https://<base-domain>/       │
                  │  preview: https://preview.<base>/<env>/│
                  └─────────────────┬─────────────────────┘
                                    │  cookie set by Cognito Hosted UI
                                    ▼
                  ┌───────────────────────────────────────┐
                  │  BFF CloudFront                        │
                  │  - WAF + security headers              │
                  │  - CFFn cookie → Authorization: Bearer │
                  │  - Lambda@Edge bff-router (preview only)│
                  │  prod:    https://bff.<base>/           │
                  │  preview: https://<env>.bff.<base>/     │
                  └─────────────────┬─────────────────────┘
                                    ▼
                  ┌───────────────────────────────────────┐
                  │  API Gateway HTTP API (v2)            │
                  │  Cognito JWT authorizer                │
                  │  Custom domain api[.|-<env>].<base>    │
                  └─────────────────┬─────────────────────┘
                                    ▼
                  ┌──────────────────────────────────────┐
                  │  Lambda (Go 1.24) — read + command   │
                  │  lambdas/<service>/main.go            │
                  └────┬───────────┬──────────────────┬───┘
                       │           │                  │
                       ▼           ▼                  ▼
            ┌────────────────┐ ┌──────────┐ ┌────────────────────┐
            │ DynamoDB       │ │ Bedrock  │ │ EventBridge bus    │
            │ <project>-items│ │ Haiku/   │ │ pipeline-<env>     │
            │ [-<env>]       │ │ Sonnet   │ │                    │
            └────────────────┘ └──────────┘ └────────────────────┘
                                                       ▲   ▲
                                                       │   │
                                ┌──────────────────────┘   │
                                │  EventBridge Scheduler   │
                                │  hourly (discovery)      │
                                │  weekly (tuners)         │
                                └──────────────────────────┘
                                            │
                                            ▼
                                    ┌─────────────────┐
                                    │ Worker Lambdas  │
                                    │ (Go) consume    │
                                    │ via SQS + DLQs  │
                                    └────────┬────────┘
                                             │
                  ┌──────────────────────────┴────────────────────────────┐
                  │                                                       │
                  ▼                                                       ▼
       ┌──────────────────────┐                          ┌────────────────────────┐
       │  Cloudflare R2       │                          │  Amazon SES            │
       │  bucket previews-<env│                          │  outreach.<base-domain>│
       │  + Cloudflare Worker │                          │  + suppression list    │
       │  + Workers KV        │                          │                        │
       │  + Rate Limiting     │                          └────────────────────────┘
       │  serves              │
       │  previews.<base-domain>/sites/{id}
       │  ↑ passcode-protected
       └──────────────────────┘
```

The admin app and the BFF use the skeleton's CloudFront+Lambda@Edge plumbing as-is. The agency-specific surfaces are: extra DynamoDB GSIs, the EventBridge bus, the worker Lambdas (audit, qualifier, spec-generator, generator, publisher, email-draft, sender, feedback, cost), Cognito, SES, and the Cloudflare R2 + Worker for **generated business previews only**.

## Repo layout (in the implementation repo, after `bin/init.sh`)

```
aws-setup/                  ← skeleton singletons; do NOT add per-env resources here
terraform/                  ← per-env stack; CI applies per branch
  api-gateway.tf, iam.tf, lambdas.tf, ssm-parameters.tf, ...   ← skeleton
  eventbridge.tf            ← added: bus + rules + scheduler
  cognito.tf                ← added: user pool + hosted UI
  ses.tf                    ← added: configuration set + suppression
  bedrock-iam.tf            ← added: bedrock:InvokeModel grouped policy
  dynamodb-items.tf         ← MUTATED from skeleton: extra GSIs added
cloudflare/                 ← added: R2 bucket + Worker (Cloudflare Terraform provider)
worker/                     ← added: Cloudflare Worker source (TypeScript) — preview server
lambdas/                    ← skeleton: Go module
  pkg/                      ← skeleton, EXTENDED:
    config/, ddb/, httpresp/, log/   ← skeleton
    bedrock/                ← added: cost-aware Bedrock client wrapper
    events/                 ← added: EventBridge envelope + publisher
    idempotency/            ← added: handler-entry idempotency
    cost/                   ← added: recordSpend / withCostCap
    politefetch/            ← added: robots-aware HTTP client
    prompts/                ← added: prompt templates + tool-use schemas
    schemas/                ← added: typed request/response/event schemas
  api-hello/                ← skeleton reference
  api-settings/             ← added: PipelineSettings CRUD
  api-queue/                ← added: review queue read API
  api-approve/              ← added: queue actions
  discover/                 ← added: discovery worker (scheduled)
  audit/                    ← added: audit worker (event-driven)
  qualifier/                ← added
  spec-generator/           ← added
  generator/                ← added: site renderer
  publisher/                ← added: R2 upload
  email-draft/              ← added
  sender/                   ← added: SES sender
  feedback/                 ← added: feedback capture
  tuner-targeting/          ← added (weekly)
  tuner-style/              ← added (weekly)
  tuner-email-tone/         ← added (weekly)
  cost-rollup/              ← added (daily)
frontend/                   ← skeleton React/Vite app, EXTENDED:
  src/
    pages/Hello.tsx         ← skeleton; replace with our admin pages
    pages/Queue.tsx         ← added
    pages/Settings.tsx      ← added
    ...
e2e-tests/                  ← skeleton Playwright; we add our flows
docs/                       ← skeleton ARCHITECTURE.md / BOOTSTRAP.md, plus our own
.ralph/                     ← copied from the spec repo (this repo)
.github/workflows/          ← skeleton; we add nothing initially
```

## Why these specific choices (revised)

- **Two-stack Terraform from the skeleton.** Singletons in `aws-setup/`, per-env in `terraform/`. Mixing them was skeleton pitfall #10 and we don't relitigate it.
- **Go 1.24 Lambdas** on `provided.al2023`. ~30% cheaper-and-faster than Node for this workload, smaller cold start, and the skeleton already wires the build (`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bootstrap .`).
- **DynamoDB single-table** `<project>-items[-<env>]`. Already provisioned by the skeleton; we extend with GSIs + `expires_at` TTL (already enabled).
- **Vite/React on CloudFront, NOT Cloudflare Pages.** The skeleton's CloudFront+S3 admin hosting works under both `<base>/` (production) and `preview.<base>/<env>/` (per-PR preview), with the **runtime-config.js** pattern that lets one build artifact serve every env. Switching to Cloudflare Pages would lose per-PR previews and the cookie→Authorization CFFn that the BFF CloudFront already gives us.
- **Cloudflare R2 + Worker for generated business previews ONLY.** R2 has zero egress fees; a single Worker with an R2 binding serves all `<websiteId>` previews under `previews.<base-domain>/sites/{websiteId}`. This is the only Cloudflare component in the system.
- **Cognito.** The skeleton doesn't ship auth, but it already gives us the cookie/Authorization plumbing. Cognito Hosted UI sets the cookie; the BFF CFFn promotes it to `Authorization: Bearer <jwt>`; the API Gateway JWT authorizer validates it. Single-operator at MVP; multi-tenant later.
- **EventBridge over Step Functions at first.** Step Functions add per-state-transition cost; we add it later (iter 10) where visibility-of-flow matters. Most consumers are fine with EventBridge fan-out via SQS.
- **Per-PR ephemeral envs are first-class.** Every iteration's acceptance is testable on a real preview env opened by the PR. We do not run integration tests against `dev` shared env; we run them against the PR env that the skeleton automatically provisions and destroys.
- **AWS Vault for the one-time bootstrap.** Per the skeleton's `docs/BOOTSTRAP.md`. The `aws-vault` profile name is set by `init.sh` (`--aws-vault-profile`).

## Environment topology

The skeleton gives us this for free:

- **`production`** — `main` branch. URLs `<base>/`, `bff.<base>/`, `api.<base>/`. PITR + deletion-protection on DynamoDB.
- **`<branch-name>` preview** — auto-spawned on PR open, destroyed on PR close. URLs `preview.<base>/<env>/`, `<env>.bff.<base>/`, `api-<env>.<base>/`. No PITR; deletion-protection off; `force_destroy=true` on per-env buckets.

`<env>` is the sanitized branch name, max 31 chars (skeleton pitfall #8). Production/main/master/prod/develop branch names are denylisted from cleanup (skeleton pitfall #13).

We mirror this for our additions:

- **EventBridge bus** `pipeline-<env>`.
- **Cloudflare R2 bucket** `previews-<env>` (production = `previews-production`).
- **SES configuration set** `outreach-<env>` (production has the verified domain; preview envs use the same verified domain but a per-env `MessageTag` so we can scope suppression).
- **Cognito user pool** is per-env (preview envs get an empty pool that doesn't take real users).

## Idempotency strategy

Every event handler keys on `eventId`. Implementation in `lambdas/pkg/idempotency/` — `WithIdempotency(ctx, eventID, fn)` writes `IDEMP#<eventId>` with `attribute_not_exists(pk)` and a 24h TTL on `expires_at` before doing work. See `stdlib/idempotency-patterns.md`.

## Observability

- Structured JSON logs via `lambdas/pkg/log/` (already in skeleton). One CloudWatch log group per Lambda with 30-day retention; Lambda has `depends_on=[<lg>]` (pitfall #5).
- EMF metrics from each handler: `pipeline.<stage>.{succeeded,failed,skipped_capped,skipped_killed}`.
- X-Ray tracing on every Lambda.
- CloudWatch alarms (defined in `terraform/`):
  - DLQ depth > 0 for > 10 min on any consumer.
  - Daily Bedrock spend > 80% of cap.
  - SES bounce rate > 5% over 24h.
  - Pipeline failures > 10% of audits in 1h.

## Skeleton-honored constraints (do not relitigate)

These are skeleton pitfall mitigations we inherit and must keep working:

1. Every `lambdas/<svc>/` directory has a placeholder `bootstrap` committed so `terraform plan` succeeds before the build job (skeleton #3).
2. Every Lambda has a CloudWatch Log Group declared in `terraform/lambda-log-groups.tf` with `retention_in_days=30` and Lambda `depends_on=[<lg>]` (skeleton #5).
3. IAM policies grouped by **permission level**, not per-resource — we add `bedrock-invoke`, `events-publish`, `ses-send` as **single** grouped policies, never per-call (skeleton #6).
4. Hardcoded domains forbidden — every domain string flows from `var.base_domain` via `local.api_domain` etc. (skeleton #7).
5. Env-name length cap of 31 chars; never widened (skeleton #8).
6. `aws_ssm_parameter` for secrets uses `lifecycle.ignore_changes=[value]` and a `PLACEHOLDER_SET_OUT_OF_BAND` default (skeleton #4).
7. Vite `base: './'` never changed (skeleton #17).
8. Singletons never added to `terraform/`; only `aws-setup/` (skeleton #10).
9. Cleanup denylist `production|main|master|prod|develop` is preserved and inherited by any new cleanup script (skeleton #13).
10. `scripts/derive-env-name.sh` is the SoT — never duplicate env-name derivation in another script or workflow step (skeleton #19).

The skeleton's `docs/ARCHITECTURE.md` § Pitfalls table is the canonical reference. When a Ralph iteration would touch any of these, it must reference the pitfall by number in the commit message.
