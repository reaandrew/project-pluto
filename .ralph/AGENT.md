# Agent Build Instructions — Website Agency Pipeline

The implementation repo is created from `reaandrew/cloud-skeleton` and customized via `bin/init.sh`. This file documents the build/test/deploy commands for that customized repo.

## Project Setup

```bash
# Tooling required on the host
# - Go 1.24+
# - Node 20+ and npm 10+
# - Terraform 1.9+
# - aws-cli v2
# - aws-vault (https://github.com/99designs/aws-vault) — used for the one-time bootstrap
# - Wrangler 3 (`npm i -g wrangler`) — Cloudflare Workers
# - Playwright deps: `npx playwright install --with-deps` (in e2e-tests/)
# - golangci-lint v1.59+

# Frontend deps
cd frontend && npm ci

# E2E deps
cd e2e-tests && npm ci

# Go modules — vendored or downloaded on first build
cd lambdas && go mod download
```

## Environment

Required env vars (in `.env.local` for local dev; in GitHub secrets for CI):

```
# AWS — set by aws-actions/configure-aws-credentials in CI
AWS_REGION=eu-west-2

# Bedrock model IDs
BEDROCK_HAIKU_MODEL_ID=anthropic.claude-haiku-4-5
BEDROCK_SONNET_MODEL_ID=anthropic.claude-sonnet-4-6

# Cloudflare
CLOUDFLARE_ACCOUNT_ID=...
CLOUDFLARE_API_TOKEN=...           # scoped: Workers, R2, Workers KV, Workers Rate Limiting

# External APIs (in AWS Secrets Manager in deployed envs)
GOOGLE_PLACES_API_KEY=...
COMPANIES_HOUSE_API_KEY=...
PAGESPEED_API_KEY=...
```

GitHub secrets per skeleton's BOOTSTRAP.md:
- `AWS_ROLE_ARN` (from `aws-setup/` `terraform output -raw github_actions_role_arn`)
- `E2E_TEST_USER`, `E2E_TEST_PASS`

Plus the new ones we add in iteration 0:
- `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`
- `GOOGLE_PLACES_API_KEY`, `COMPANIES_HOUSE_API_KEY`, `PAGESPEED_API_KEY` (also mirrored to AWS Secrets Manager)

## Running Tests

```bash
# Go (per Lambda or root)
cd lambdas && go test ./...
cd lambdas/audit && go test -race -count=1 ./...
cd lambdas && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

# Frontend
cd frontend && npm run test
cd frontend && npm run test:coverage

# Worker
cd worker && npm run test                # vitest with @cloudflare/vitest-pool-workers

# E2E (against per-PR preview URL set by CI)
cd e2e-tests && BASE_URL="$PREVIEW_URL" npx playwright test

# Local LocalStack-backed integration (Bedrock not available in LocalStack — mock it)
docker compose up -d localstack
cd lambdas && BEDROCK_MOCK=1 go test -tags=integration ./...
```

## Build

```bash
# A single Lambda — produces lambdas/<svc>/bootstrap
cd lambdas/audit
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bootstrap .

# All Lambdas at once (CI)
make build-lambdas    # added by us — iterates over lambdas/*/main.go and builds each

# Frontend
cd frontend && npm run build   # outputs to dist/

# Worker
cd worker && npm run build     # wrangler build (or wrangler deploy --dry-run)
```

## Lint / Typecheck / Format

```bash
# Go
cd lambdas && gofmt -l .                    # must be empty
cd lambdas && go vet ./...
cd lambdas && golangci-lint run --timeout=5m

# Frontend
cd frontend && npm run typecheck
cd frontend && npm run lint
cd frontend && npm run format:check

# Worker
cd worker && npm run typecheck && npm run lint

# Terraform
cd aws-setup && terraform fmt -recursive -check && terraform validate
cd terraform && terraform fmt -recursive -check && terraform validate
cd cloudflare && terraform fmt -recursive -check && terraform validate

# Security
checkov -d terraform/ -d aws-setup/ -d cloudflare/
tfsec terraform/ aws-setup/ cloudflare/
gitleaks detect --no-banner
```

## Local Dev

```bash
# Frontend dev server (Vite, port 5173)
cd frontend && npm run dev

# A single Lambda invoked locally
cd lambdas/audit && go run . < testdata/sample-event.json

# Worker dev (Miniflare via wrangler)
cd worker && npm run dev   # wrangler dev — local KV, R2, Rate Limiting bindings
```

## Deploy

The skeleton's `.github/workflows/deploy.yml` is the primary deploy path. Manual deploys exist for one-time bootstrap only.

```bash
# One-time AWS bootstrap (read docs/BOOTSTRAP.md first)
cd aws-setup
aws-vault exec <profile> -- terraform init
aws-vault exec <profile> -- terraform apply

# Per-env Terraform (CI does this; manual is for emergencies)
cd terraform
terraform init -backend-config="bucket=<project>-terraform-state-<acct>" \
  -backend-config="key=terraform/<project>/${ENV}/terraform.tfstate" \
  -backend-config="region=eu-west-2"
terraform apply -var="environment=${ENV}"

# Cloudflare (Terraform + Wrangler)
cd cloudflare && terraform apply
cd worker && wrangler deploy --env "${ENV}"
```

## Skeleton-bootstrap reminder

When implementing iteration 0:
1. Run `bin/init.sh` ONCE on a fresh repo from the template; verify with `--dry-run` first.
2. Copy `.ralph/` and `CLAUDE.md` from the spec repo (`reaandrew/ai-website-agency`) into the new repo.
3. Follow `docs/BOOTSTRAP.md` end-to-end (state bucket → first apply → NS records → second apply → verify SSM contract → set secrets → first deploy).
4. Verify per-PR env works (open + close a no-op PR; cleanup must complete).
5. Run iteration 0.B (CI efficiency) so subsequent PRs land fast.
6. Then begin the agency-specific extensions in iteration 0.C onwards.

## Key Learnings (skeleton-derived)

- **Bootstrap placeholder is intentional.** Every `lambdas/<svc>/` ships a placeholder `bootstrap` (a one-line shell script) so `terraform plan` succeeds before the build job runs. Pitfall #3.
- **CloudWatch log groups before Lambda.** Always declare the log group in `terraform/lambda-log-groups.tf` with `retention_in_days=30` and add `depends_on=[<lg>]` to the Lambda. Pitfall #5.
- **IAM grouped by permission level.** Adding a new DynamoDB table = append actions to existing `dynamodb_read` / `dynamodb_write` policies, never per-table. Pitfall #6.
- **Hardcoded domains forbidden.** Everything flows from `var.base_domain` → `local.api_domain`/`local.bff_domain`/`local.frontend_url`. Pitfall #7.
- **Env-name 31-char cap.** Branch names truncated by `scripts/derive-env-name.sh`. Don't widen. Pitfall #8.
- **SSM `lifecycle.ignore_changes=[value]`** for any secret managed out-of-band. Pitfall #4.
- **Vite `base: './'`** stays. Pitfall #17.
- **Singletons live in `aws-setup/`.** Never in `terraform/`. Pitfall #10.
- **Cleanup denylist** — `production|main|master|prod|develop` cannot be destroyed. Inherit this denylist in any new cleanup tooling. Pitfall #13.
- **`scripts/derive-env-name.sh`** is the single source of truth for env-name. Source it; don't reimplement. Pitfall #19.

## Project-specific learnings (added by us, evolves with implementation)

- **Bedrock IAM**: ONE `bedrock-invoke` policy in `terraform/bedrock-iam.tf` granting `bedrock:InvokeModel` on the two model ARNs. Never per-call. Inherits the skeleton's "grouped by permission level" pattern (pitfall #6).
- **EventBridge bus per env**: name is `pipeline${local.env_suffix}`. Production = `pipeline`. Preview = `pipeline-<branch>`.
- **Passcode cleartext is a secret.** It lives KMS-encrypted in `Website.passcodeCipher` (TTL 7 days via `passcodeRevealableUntil`), and in `EmailDraft.body`. Never logs, never events, never X-Ray.
- **Workers KV `PREVIEW_PASSCODES`** holds `passcode:<websiteId>` → argon2id hash. Cloudflare Terraform provider provisions; the publisher Lambda writes via the Cloudflare API.

## Feature Development Quality Standards

**CRITICAL**: All new features MUST meet the following before being considered complete.

### Testing Requirements
- 85% line coverage on new code; 100% pass rate.
- Unit tests for business logic; integration tests for any handler that touches DynamoDB/Bedrock/EventBridge/SES.
- E2E test for any user-visible workflow change (Playwright).
- Bedrock prompt assembly: snapshot tests on the assembled tool-use payload.
- Worker passcode flow: integration test against Miniflare covering correct passcode, wrong passcode, rate-limit, signed cookie reuse, revocation propagation.

### Git Workflow
- Conventional commits: `feat(scope):`, `fix(scope):`, `docs:`, `test:`, `refactor:`, `chore:`.
- Feature branches: `feature/<iteration>-<slug>` (e.g., `feature/2-audit-engine`).
- One iteration per PR where practical.
- Every PR must:
  - keep `.ralph/fix_plan.md` updated (boxes ticked, follow-ups added),
  - update `.ralph/AGENT.md` if commands changed,
  - leave specs in `.ralph/specs/` consistent with shipped code,
  - reference the relevant skeleton pitfall # in the commit message if it's near a mitigated area.

### Documentation
- All shared types and event payloads documented in `lambdas/pkg/events/` + `lambdas/pkg/schemas/`.
- Every Lambda has a header doc-comment with: trigger, idempotency key, retry policy, Bedrock cost class.
- Bedrock prompt templates live in `lambdas/pkg/prompts/` and are referenced by ID in handlers — never inline a prompt string in handler code.

### Feature Completion Checklist
- [ ] All tests pass
- [ ] Coverage ≥ 85%
- [ ] `gofmt`, `go vet`, `golangci-lint` clean
- [ ] `npm run typecheck && npm run lint` clean (frontend, worker)
- [ ] `terraform fmt -check && terraform validate` clean
- [ ] Idempotency proven (replay event in test)
- [ ] Cost recorded if a paid API was called
- [ ] Capacity gate respected (`pipelineEnabled` + per-stage flag)
- [ ] No fake-fact violations from `10-quality-rules.md`
- [ ] No passcode cleartext in logs / events / X-Ray
- [ ] No regression of any skeleton pitfall mitigation
- [ ] `.ralph/fix_plan.md` updated
- [ ] `.ralph/AGENT.md` updated if needed
- [ ] CI green (skeleton's `deploy.yml` + `cloudflare.yml`)
- [ ] Working demo: per-PR preview env reachable, smoke check passes
