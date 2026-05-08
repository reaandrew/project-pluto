# Project Instructions for Claude

## AWS Authentication

Always use `aws-vault` with profile `personal_iphone` for AWS commands.

```bash
aws-vault exec personal_iphone -- <command>
```

Examples:
- `aws-vault exec personal_iphone -- terraform apply`
- `aws-vault exec personal_iphone -- aws s3 ls`
- `aws-vault exec personal_iphone -- aws sts get-caller-identity`  → expects account `276447169330`

## Repo layout

- `/aws-setup` — one-time bootstrap (zone, certs, CloudFronts, OIDC, IAM role). Applied manually with aws-vault. OWN tfstate at `aws-setup/terraform.tfstate` in `website-agency-terraform-state-276447169330`.
- `/terraform` — per-env stack applied by CI. State key `terraform/website-agency/{env}/terraform.tfstate` per env.
- `/lambdas` — Go Lambdas on `provided.al2023`. Shared code in `lambdas/pkg/`.
- `/frontend` — React + Vite + TypeScript. `vite.config.ts` MUST keep `base: './'` for path-prefix preview to work.
- `/scripts` — pipeline plumbing. `derive-env-name.sh` is the SINGLE source of truth for env-name derivation (sourced by every workflow step).
- `/e2e-tests` — Playwright.
- `/.github/workflows` — `deploy.yml` is the main pipeline.

## Per-environment URL contract

| | Production (`main`) | Preview (`feat-x`) |
|---|---|---|
| Frontend | https://agency.andrewreaassociates.com | https://preview.agency.andrewreaassociates.com/feat-x/ |
| BFF | https://bff.agency.andrewreaassociates.com | https://feat-x.bff.agency.andrewreaassociates.com |
| API | https://api.agency.andrewreaassociates.com | https://api-feat-x.agency.andrewreaassociates.com |

## Build commands

```bash
# Lambda (per Lambda dir)
cd lambdas/api-hello
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bootstrap .

# Frontend
cd frontend && npm run build   # outputs to dist/

# Terraform (validate only, no apply)
cd aws-setup && terraform fmt -recursive -check && terraform validate
cd terraform   && terraform fmt -recursive -check && terraform validate
```

## Things to never do

- Never commit a real `bootstrap` binary — only the placeholder shell script that prints "placeholder".
- Never use `terraform workspace` — environments are separated by tfstate `key`, not by workspace.
- Never hardcode a domain literal anywhere except `var.base_domain`'s default. Everything else flows from `local.api_domain` / `local.bff_domain` / `local.frontend_url` derived in `terraform/shared-infrastructure-data.tf`.
- Never use `overwrite=true` on `aws_ssm_parameter` for secrets. Use `lifecycle { ignore_changes = [value] }` and import.
- Never put singleton resources (CloudFront, hosted zone, certs) in `terraform/`. They live in `aws-setup/`.
- Never put `force_destroy = true` on the production frontend bucket.
- Never run `terraform destroy` against `production`/`main`/`master`/`prod`/`develop`. The cleanup script and the cleanup workflow job both refuse those names.

## Pitfalls already mitigated (do NOT relitigate)

| Pitfall | Mitigation |
|---|---|
| S3 force_destroy blocked by missing IAM perms | `aws-setup/main.tf` `s3-access` policy grants `DeleteObjectVersion` etc.; `cleanup-environment.sh` empties versioned + multipart first |
| Dummy bootstrap missing breaks `terraform plan` on fresh branch | Placeholder committed at `lambdas/api-hello/bootstrap`; CI re-creates dummies in provision-branch and cleanup jobs |
| Lambda auto-creates no-retention log groups | `terraform/lambda-log-groups.tf` declares them with retention=30; Lambda has `depends_on=[<lg>]` |
| IAM 10KB inline policy limit | Policies grouped by permission level (read/write), never per-table; ARN wildcards |
| Stale tfstate after destroy | cleanup workflow `aws s3 rm s3://${TF_STATE_BUCKET}/terraform/website-agency/${ENVIRONMENT}/ --recursive` |
| Cross-workflow env-name derivation drift | `scripts/derive-env-name.sh` sourced everywhere |
| Per-branch CloudFront (15min provision + 15min destroy) | SHARED CloudFront for preview + BFF; only API GW custom domain is per-branch |

See `docs/ARCHITECTURE.md` for the full pitfall→mitigation matrix.
