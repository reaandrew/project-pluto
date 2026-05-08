# Architecture

## Topology

```
                          User
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
       ai-website-agency.lev…   bff.ai-website-agency.lev…  preview.ai-website-agency.lev…/<env>/
              │             │                       │
              ▼             ▼                       ▼
       ┌────────────┐ ┌──────────────┐    ┌────────────────────┐
       │ CF prod-fe │ │ CF bff       │    │ CF preview-fe      │
       │  + WAF     │ │  + WAF       │    │  + WAF             │
       │  + sec hdr │ │  + cookie→   │    │  + Lambda@Edge     │
       └─────┬──────┘ │    auth CFFn │    │    preview-router  │
             │OAC     │  + Lambda@Ed │    │   (path → S3 prefix)│
             ▼        │  bff-router  │    └─────────┬──────────┘
       ┌────────────┐ │  (host →     │              │ OAI
       │ S3 prod-fe │ │   api-<env>) │              ▼
       └────────────┘ └──────┬───────┘    ┌────────────────────┐
                             │             │ S3 preview-shared │
                  api[.|-<env>].ai-website-agency…  │ /<env>/...        │
                             ▼             └────────────────────┘
                    ┌──────────────────┐
                    │ API Gateway v2   │   <── per-env custom domain
                    │ HTTP API         │       (api[.|-<env>])
                    └────────┬─────────┘
                             ▼
                    ┌──────────────────┐
                    │ Lambda           │   ai-website-agency-api-hello[-<env>]
                    │ (Go, prov.al2023)│   provided.al2023, bootstrap
                    └────────┬─────────┘
                             ▼
                    ┌──────────────────┐
                    │ DynamoDB         │   ai-website-agency-items[-<env>]
                    │ PAY_PER_REQUEST  │   PITR + del-prot prod-only
                    └──────────────────┘

Singletons (one copy in aws-setup/):
  - hosted zone agency.techar.ch
  - ACM cert wildcard *.agency.techar.ch (one in eu-west-2 for API GW, one in
    us-east-1 for CloudFront; both also cover *.bff.agency.techar.ch)
  - 3× CloudFront distributions (prod-fe, preview-fe, bff)
  - OIDC provider for GitHub Actions
  - GitHub Actions IAM role github-actions-ai-website-agency
  - Lambda@Edge: ai-website-agency-preview-router, ai-website-agency-bff-origin-router
  - CloudFront Function: ai-website-agency-cookie-to-auth
  - WAF WebACL
  - S3: ai-website-agency-frontend-production, ai-website-agency-frontend-preview-shared-<acct>
```

## Pitfalls → mitigations

| # | Past failure | Mitigation in this codebase |
|---|---|---|
| 1 | S3 `force_destroy` blocked by missing IAM `s3:DeleteObjectVersion` etc. (smm 8c01880, tripwire 1db6f99) | `force_destroy=true` on every per-env bucket and the preview-shared bucket; `aws-setup/main.tf` `s3-access` policy grants `DeleteObjectVersion`, `ListBucketVersions`, `GetObjectVersion`, `AbortMultipartUpload`, `ListBucketMultipartUploads`; `cleanup-environment.sh` empties versioned + multipart **before** `terraform destroy`. |
| 2 | DynamoDB tables wiped (no PITR/deletion protection) (smm 1f225d5) | `terraform/dynamodb-items.tf` enables both gated on `local.is_production`. |
| 3 | Missing dummy bootstrap files break `terraform plan` on fresh branch (tripwire bc96561) | Placeholder `bootstrap` script committed to `lambdas/api-hello/`; provision-branch + cleanup workflow steps recreate dummies before plan/destroy. |
| 4 | SSM `overwrite=true` race wipes secrets (tripwire 073ae18) | `terraform/ssm-parameters.tf` uses `lifecycle.ignore_changes=[value]` and `value = "PLACEHOLDER_SET_OUT_OF_BAND"`; deploy job uses `terraform import …\|\|true` loop for params owned outside Terraform. |
| 5 | Lambda auto-creates no-retention log groups (tripwire c065c37) | `terraform/lambda-log-groups.tf` declares each LG explicitly with `retention_in_days=30`; Lambda has `depends_on=[<lg>]`. |
| 6 | IAM 10KB inline policy limit (smm 34d7d25) | `aws-setup/main.tf` splits GH-actions IAM into 12 `aws_iam_role_policy` resources, never one giant doc. `terraform/iam.tf` groups DynamoDB by permission level (read/write), not per-table; uses ARN wildcards. |
| 7 | Hardcoded domains route prod traffic to prod from previews (tripwire e119c83, 0b817e2) | Every domain string flows from `var.base_domain` via `locals` in `shared-infrastructure-data.tf`. |
| 8 | Long branch names break IAM/resource name limits | `env_sanitized = substr(..., 0, 31)`; Lambda@Edge router regex bounds `[a-z0-9-]{1,31}`; `derive-env-name.sh` `cut -c1-31`. |
| 9 | Per-env state collisions (one tfstate file for all envs) | Backend block omits `key`; CI passes `-backend-config="key=terraform/ai-website-agency/${env}/terraform.tfstate"` per apply. |
| 10 | Per-env resources mixed into shared singletons in same stack (tripwire b431b45) | `aws-setup/` owns ALL singletons; `terraform/` consumes via `data.aws_ssm_parameter` only. |
| 11 | Terraform replacement order errors (tripwire 1d3fc8e, b316db5) | `lifecycle.create_before_destroy = true` on both ACM certs. |
| 12 | ACM validation race | `aws_acm_certificate_validation` blocks before SSM publishes the ARN; `terraform/` reads SSM, so cross-stack ordering is enforced. |
| 13 | Branch name collisions destroy production (smm cleanup bug) | `cleanup-environment.sh` denylist `production\|main\|master\|prod\|develop` AND the `cleanup` workflow job has the same denylist as its FIRST step (defense in depth). |
| 14 | Stale tfstate after broken destroy (smm 98743ae) | `cleanup` job has explicit `aws s3 rm s3://.../<env>/ --recursive` step after `terraform destroy` succeeds. |
| 15 | Docker pull flakes break E2E (tripwire 4f31854, 294d65f) | Every `docker pull` / `compose pull` wrapped in 5-attempt × 15s retry loop. |
| 16 | CloudFront invalidation flakes | `deploy-frontend.sh` exponential backoff (2,4,8,16,32s). |
| 17 | Vite asset paths wrong under preview path-prefix | `vite.config.ts` `base: './'`; relative paths everywhere. |
| 18 | Per-env build doubles CI time | Build frontend ONCE per pipeline run; per-env work is just `s3 sync` + `runtime-config.js` write. |
| 19 | Cross-workflow env-name derivation drift (tripwire/smm have already drifted) | `scripts/derive-env-name.sh` is the only place that derives the env. |
| 20 | Per-branch CloudFront (15min provision + 15min destroy each) | SHARED CloudFront for preview frontend + BFF; per-branch resources are only API Gateway custom domain (~1min) and Lambda + DynamoDB (instant). |

## Why these design choices

**Two Terraform stacks (`aws-setup/` + `terraform/`).** Singletons (zone, certs, CloudFronts) live in `aws-setup/` and never touch CI. Per-env (Lambda, API GW, DDB, S3 uploads) is in `terraform/` with a separate tfstate per env. This is the lesson from tripwire b431b45 — mixing them causes shared resources to thrash on every branch deploy.

**Runtime config, not build-time bake-in for the frontend.** `index.html` loads `/runtime-config.js` BEFORE the bundle. The Vite build artifact is identical for every env; CI writes the per-env `runtime-config.js` next to `index.html` immediately after `aws s3 sync`. This means **one `npm run build` per pipeline run**, not one per env — preview deploys are pure S3 sync.

**`vite.config.ts` `base: './'` is load-bearing.** With `base: '/'` (the default) the bundle requests `/assets/index-abc.js`, which the path-prefix preview can't resolve. Relative paths let the same `dist/` work under both `/` (production) and `/<env>/` (preview).

**BFF distribution uses Lambda@Edge for preview, not for production.** The production BFF distribution has a fixed origin (`api.agency.techar.ch`); no edge function. The preview BFF distribution (`*.bff.agency.techar.ch`) uses a CloudFront Function on viewer-request to stamp `x-original-host`, then a Lambda@Edge on origin-request to inspect that header and rewrite the origin to `api-<env>.agency.techar.ch`. This way one distribution serves every preview branch without touching CloudFront on PR open/close.
