# Claude Code instructions — `terraform/`

Per-environment stack (CI-applied). One state file per env at `s3://…-terraform-state-<acct>/terraform/ai-website-agency/<env>/terraform.tfstate`. The CI deploy job applies with `-var=environment=<env>`.

`aws-setup/` is the **singleton** stack (manual `aws-vault`-applied, owned by ops): Route53 zone, ACM certs, CloudFront/Lambda@Edge, GitHub-Actions OIDC role, WAF. Never add per-env resources there.

## Read first
- `.ralph/specs/01-architecture.md` — stack split, region pinning
- `.ralph/specs/02-data-model.md` — DynamoDB tables + GSIs
- `.ralph/specs/05-capacity-and-cost.md` — `PipelineSettings` singleton
- `docs/ARCHITECTURE.md` § Pitfalls — the 20 cloud-skeleton mitigations
- `.ralph/specs/stdlib/skeleton-conventions.md`

## Conventions
- All per-env resources name themselves with `local.env_suffix` (defined in `terraform/env-suffix.tf`); `production` has no suffix, everything else gets `-<env>`.
- IAM: ONE Lambda execution role + policies grouped **by permission level** (skeleton pitfall #6 — long branch names + per-table policies overflow the 10KB inline-policy limit). Add new actions to existing groups; never create per-table policy resources.
- No hardcoded domain — use `var.base_domain`, `var.parent_domain`, or read from SSM (`/ai-website-agency/route53/zone_id` etc).
- `force_destroy = !local.is_production` for any S3 bucket / DynamoDB table that should clean up on per-PR teardown.

## Don't
- Add CloudFront resources here — those live in `aws-setup/` (skeleton pitfall #20: per-branch CloudFront avoidance).
- Add a backend block — the bucket name is hardcoded in `aws-setup/main.tf` and CI provides `-backend-config="key=…"`.
- Use `count` for env switching — use `for_each` or conditional locals so refactors don't shuffle resource indices.
- Edit `scripts/derive-env-name.sh` (skeleton pitfall #19 — single source of truth for branch → env-name).
