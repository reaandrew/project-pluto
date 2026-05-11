# project-pluto

An AI-assisted outbound pipeline for redesigning small-business websites. It
continuously finds businesses with weak sites, audits them, generates a private
redesign preview for the qualified ones, and surfaces the top candidates per
day in a human review queue. The operator approves, edits, or rejects; on
approval, an outreach email with the preview link is drafted and sent via SES.

Operator decisions on lead criteria, site design, and email copy flow back
into targeting, generation, and tone tuners so the pipeline measurably
improves with use.

This is the **implementation repo**. The canonical specification lives at
[`reaandrew/ai-website-agency-spec`](https://github.com/reaandrew/ai-website-agency-spec).

## Status

Under construction. Tracked in [`.ralph/fix_plan.md`](.ralph/fix_plan.md):

- Iteration 0.A → 0.F shipped — bootstrap, CI rewrite, agency-specific Terraform
  (EventBridge, Cognito, SES, Bedrock IAM, DynamoDB GSIs, KMS), the Cloudflare
  R2 + Worker preview-hosting layer, the kill-switch + cost-cap controls, and
  the cost-rollover scheduled Lambda.
- Iteration 0.G in flight — admin shell routing.
- Iterations 1–11 — Targeting Profile, Discovery, Audit, Qualification,
  Spec Generator, Preview Generator, Review Queue, Email, Feedback Tuners,
  Production Hardening.

Each iteration ends with a working demo. No half-built infra survives between
iterations.

## What's in here

```
.ralph/specs/   — architecture, data model, events, prompts, iteration plan, quality rules
.ralph/         — Claude-Code operating instructions, fix plan, agent + command definitions
.claude/        — review subagents, slash commands, hooks
aws-setup/      — one-time bootstrap stack (Route53 zone, ACM certs, CloudFronts, OIDC role)
terraform/      — per-env stack applied by CI (Lambdas, API GW, DynamoDB, Cognito, SES, EventBridge, KMS)
cloudflare/     — Terraform for the R2 + Worker passcode-gated preview hosting
worker/         — Cloudflare Worker source
lambdas/        — Go Lambda services + shared pkg/ libraries (events, cost, idempotency, killswitch, …)
frontend/       — Vite + React + TypeScript admin shell
e2e-tests/      — Playwright suite (runs on the runner, no Docker)
docs/           — ARCHITECTURE.md, BOOTSTRAP.md
scripts/        — derive-env-name, wait-for-endpoint, deploy-frontend, cleanup-environment
.github/        — Actions workflows (deploy, destroy, e2e-tests, security)
```

## Architecture at a glance

| Layer | Tech |
|---|---|
| Discovery + Audit + Qualify + Generate consumers | Go 1.24 on AWS Lambda (`provided.al2023`), SQS-driven, idempotent |
| LLM | Amazon Bedrock — Haiku 4.5 for short copy + triage, Sonnet 4.6 for spec + headlines |
| Data | DynamoDB (single table, `pk`+`sk`, GSI1/2/3), PAY_PER_REQUEST, PITR in prod |
| Events | EventBridge custom bus + archive; consumers retry via per-Lambda DLQs |
| Schedule | EventBridge Scheduler (hourly discovery, daily cost-rollover, weekly tuners) |
| Admin API | API Gateway v2 HTTP API + JWT authorizer (Cognito Hosted UI, operator group) |
| Admin UI | React SPA on CloudFront + S3 (production + per-PR preview envs) |
| Generated previews | Cloudflare R2 + a single Worker with KV + Rate Limiting (passcode-gated) |
| Email | SES with configuration set, suppression list, bounce/complaint feedback |
| Secrets at rest | KMS CMK for passcode-cleartext envelope encryption (`publisher` writes, `email-draft` decrypts, wiped 24h post-send) |

Full topology + the data model + the event flow + the iteration plan all live
under [`.ralph/specs/`](.ralph/specs/). The
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) file has the diagram and the
running list of skeleton-pitfalls the substrate mitigates.

## Operating principles

1. **Capacity-aware by default.** Discovery is cheap and runs broadly; preview
   generation and outreach are expensive and run narrowly. Operator attention
   is the most expensive resource — protect it with caps.
2. **Composition over freestyle.** Bedrock outputs structured JSON; code
   renders known-safe components. No invented testimonials, awards, staff,
   prices, certifications, or affiliations. See
   [`.ralph/specs/10-quality-rules.md`](.ralph/specs/10-quality-rules.md).
3. **Cost-aware AI.** Haiku for triage; Sonnet only for spec + headlines.
   Always cache, always cap.
4. **Idempotent everywhere.** Every event handler is replay-safe. Every paid
   API call has a cache. Every consumer has a DLQ.
5. **Feedback is product.** If a stage generates an artifact, the operator's
   override on that artifact is captured in the same iteration. The weekly
   tuners turn those captures into adjustments to targeting / style guides /
   email tone.

## URL contract per environment

| | Production (`main`) | Preview (PR branch `feat-x`) |
|---|---|---|
| Frontend | `https://agency.techar.ch/` | `https://preview.agency.techar.ch/feat-x/` |
| BFF | `https://bff.agency.techar.ch/` | `https://feat-x.bff.agency.techar.ch/` |
| API | `https://api.agency.techar.ch/` | `https://api-feat-x.agency.techar.ch/` |

Per-PR envs spawn on open and tear down on close. The two preview-side
CloudFront distributions and the wildcard ACM cert are shared singletons in
`aws-setup/`.

## Substrate

The repo started from the `reaandrew/cloud-skeleton` template — a production-
grade AWS substrate with a known-pitfall mitigation table, per-PR ephemeral
envs, OIDC-only CI deploys, and the gated Actions pipeline (lint + types +
unit + secret-scan + SAST + SCA + IaC scan + smoke + E2E + DAST + a11y +
load). The pipeline and the agency-specific Lambdas + Terraform on top of
that substrate are this repo's contribution.

## Build / test locally

```bash
# Go Lambdas
cd lambdas && go test ./... && golangci-lint run --timeout=5m ./...

# Frontend
cd frontend && npm ci && npm run typecheck && npm run lint && npm run test && npm run build

# Terraform (validation only; apply happens via CI / aws-vault)
cd terraform && terraform fmt -check -recursive && terraform validate
```

See [`docs/BOOTSTRAP.md`](docs/BOOTSTRAP.md) for the one-time AWS bootstrap if
you want to actually deploy this rather than read it.

## License

No license declared yet. Treat the code as source-available reference material
until a `LICENSE` file lands.
