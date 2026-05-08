# stdlib — cloud-skeleton conventions

The implementation repo is created from `reaandrew/cloud-skeleton`. This file lists the skeleton-derived conventions you must keep working. The full table with historical context lives in the implementation repo at `docs/ARCHITECTURE.md` § "Pitfalls → mitigations" — reference pitfall numbers in commit messages when you touch any of these.

## Two-stack Terraform

- **`aws-setup/`** — singletons. Hosted zone, ACM certs, OIDC, GitHub Actions IAM role, CloudFront distributions, Lambda@Edge, CloudFront Functions, WAF, S3 frontend buckets, CMK for passcode cleartext, the project KMS keys. **Applied manually with `aws-vault`.** Owns its own tfstate at `aws-setup/terraform.tfstate` in the project state bucket.
- **`terraform/`** — per-env stack. Lambdas, API Gateway, DynamoDB, EventBridge, SES, Cognito, SQS, per-env S3 buckets. Applied by CI per branch. Backend block omits `key`; CI passes `-backend-config="key=terraform/<project>/<env>/terraform.tfstate"`.

**Never** put singletons in `terraform/`. Pitfall #10.

## Bootstrap placeholders

Every `lambdas/<svc>/` ships a placeholder `bootstrap` script (one-line shell) committed at zero-byte size 0+ so `terraform plan` succeeds before the build job has produced real binaries. The placeholder is overwritten by CI in the build step. Pitfall #3.

When you add a new Lambda:
```bash
cat > lambdas/<new>/bootstrap <<'EOF'
#!/bin/sh
echo "placeholder — overwritten by CI build step"
EOF
chmod +x lambdas/<new>/bootstrap
git add lambdas/<new>/bootstrap
```

## Environment naming

- `production` for the `main` branch.
- Sanitized branch name (lowercase, `[a-z0-9-]`, max 31 chars) for any other branch.
- Source of truth: `scripts/derive-env-name.sh`. Source it from every workflow step. Don't reimplement.
- Cleanup denylist: `production|main|master|prod|develop`. Inherit this list in any new cleanup tooling. Pitfall #13.

## CloudWatch log groups

Every Lambda has a log group declared in `terraform/lambda-log-groups.tf` with `retention_in_days=30`. The Lambda has `depends_on=[<lg>]`. Pitfall #5.

When you add a new Lambda:
```hcl
# terraform/lambda-log-groups.tf
resource "aws_cloudwatch_log_group" "<svc>" {
  name              = "/aws/lambda/<project>-<svc>${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}
```

## IAM grouped by permission level

`terraform/iam.tf` has policies named by permission level (e.g. `dynamodb-read`, `dynamodb-write`, `ssm-read`, `xray`, `cloudwatch-logs`, `bedrock-invoke`, `events-publish`, `ses-send`). Adding a new resource = append to the existing grouped policy. Never per-resource policy. Pitfall #6.

## Hardcoded domains forbidden

Every domain string flows from `var.base_domain` via `local.api_domain`, `local.bff_domain`, `local.frontend_url`, `local.preview_url` derived in `terraform/shared-infrastructure-data.tf`. Pitfall #7.

## SSM secrets

Out-of-band secrets (Cognito user passwords, Bedrock-related opaque values, etc.) use:
```hcl
resource "aws_ssm_parameter" "<name>" {
  name  = "/<project>/<env>/<key>"
  type  = "SecureString"
  value = "PLACEHOLDER_SET_OUT_OF_BAND"
  lifecycle {
    ignore_changes = [value]
  }
}
```
The CI deploy job uses `terraform import … || true` for params owned outside Terraform. Pitfall #4.

## Vite `base: './'`

`frontend/vite.config.ts` has `base: './'`. **Never** change to `'/'`. With `'/'` the bundle requests `/assets/index-abc.js` which the path-prefix preview cannot resolve. Pitfall #17.

## One frontend build per pipeline

The Vite build artifact is identical for every env. CI builds **once** and writes the per-env `runtime-config.js` next to `index.html` immediately after `aws s3 sync`. Pitfall #18.

## Per-env state isolation

State key is per-env: `terraform/<project>/<env>/terraform.tfstate`. `terraform init` always passes `-backend-config="key=…"`. Never use `terraform workspace`. Pitfall #9.

## S3 force_destroy

Every per-env bucket and the preview-shared bucket has `force_destroy=true`. The skeleton's `aws-setup/main.tf` `s3-access` policy grants `DeleteObjectVersion`, `ListBucketVersions`, `GetObjectVersion`, `AbortMultipartUpload`, `ListBucketMultipartUploads`. Production frontend bucket does NOT have `force_destroy`. Pitfall #1.

## DynamoDB protection prod-only

`point_in_time_recovery.enabled = local.is_production` and `deletion_protection_enabled = local.is_production`. Pitfall #2.

## ACM `create_before_destroy`

Both ACM certs (`eu-west-2` for API GW, `us-east-1` for CloudFront) have `lifecycle { create_before_destroy = true }`. Pitfall #11.

## Cleanup ordering

`scripts/cleanup-environment.sh` empties versioned + multipart S3 BEFORE `terraform destroy`. After `terraform destroy` completes, an explicit `aws s3 rm s3://${TF_STATE_BUCKET}/terraform/<project>/${ENVIRONMENT}/ --recursive` removes stale tfstate. Pitfall #14.

## Docker pull retry

Every `docker pull` and `docker compose pull` in CI is wrapped in a 5-attempt × 15s retry. Pitfall #15.

## CloudFront invalidation backoff

`scripts/deploy-frontend.sh` retries CloudFront invalidations with exponential backoff (2,4,8,16,32s). Pitfall #16.

## Per-branch CloudFront avoidance

Per-branch resources are only API Gateway custom domain (~1 min provision/destroy) and Lambda + DynamoDB (instant). The preview-frontend CloudFront and BFF preview CloudFront are **shared singletons** in `aws-setup/`. Per-branch CloudFront caused 15-min blowup times. Pitfall #20.

## When you're tempted to relitigate

If a test or implementation change appears to require violating one of the above, the answer is: **fix the implementation, not the rule**. The 20 mitigations are the cumulative result of 6 production attempts at this architecture; relitigating any one of them is wasted iteration. Reference the pitfall number in your commit message.
