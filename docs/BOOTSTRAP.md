# Bootstrap Runbook

One-time procedure to bring up the ai-website-agency project. Do this once per AWS account.

The parent zone `techar.ch` is hosted in this same AWS account, so `aws-setup/route53-delegation.tf` adds the NS records for `agency.techar.ch` automatically as part of the apply. **Bootstrap is a single `terraform apply` — no Ctrl-C / second-apply dance.** A fallback for cross-account / external-DNS parents is at the bottom.

## Pre-flight

```bash
aws-vault exec personal_iphone -- aws sts get-caller-identity
# Expected:
#   "Account": "276447169330"
#   "Arn": "arn:aws:iam::276447169330:user/andy.rea"

aws-vault exec personal_iphone -- aws route53 list-hosted-zones \
  --query "HostedZones[?Name=='techar.ch.']"
# Expected: one zone matching `techar.ch.` (private=false).
```

## Step 1 — Create the Terraform state bucket out-of-band

Terraform can't bootstrap its own backend, so the bucket exists before any `terraform init`.

```bash
aws-vault exec personal_iphone -- aws s3api create-bucket \
  --bucket ai-website-agency-terraform-state-276447169330 \
  --region eu-west-2 \
  --create-bucket-configuration LocationConstraint=eu-west-2

aws-vault exec personal_iphone -- aws s3api put-bucket-versioning \
  --bucket ai-website-agency-terraform-state-276447169330 \
  --versioning-configuration Status=Enabled

aws-vault exec personal_iphone -- aws s3api put-bucket-encryption \
  --bucket ai-website-agency-terraform-state-276447169330 \
  --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'

aws-vault exec personal_iphone -- aws s3api put-public-access-block \
  --bucket ai-website-agency-terraform-state-276447169330 \
  --public-access-block-configuration \
    BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
```

## Step 2 — Apply `aws-setup/`

```bash
cd aws-setup
aws-vault exec personal_iphone -- terraform init
aws-vault exec personal_iphone -- terraform apply
```

What happens, in order:

1. New hosted zone `agency.techar.ch` is created with assigned nameservers.
2. NS records for `agency.techar.ch` are added to the parent zone `techar.ch` (same account; `route53-delegation.tf`).
3. ACM certificates are requested with DNS validation; CNAME records are written into the new zone.
4. ACM polls public DNS for the validation CNAMEs. With NS delegation in place + DNS propagation (TTL 300s), this normally takes 2–10 min.
5. Certs transition to `ISSUED`; SSM params written; CloudFront distributions created. (Full CloudFront propagation lags another 15–30 min globally — but `terraform apply` returns when AWS accepts the config.)

Expected total: **10–20 minutes**, mostly DNS / ACM waiting.

Verify delegation worked:

```bash
dig +short NS agency.techar.ch @8.8.8.8
dig +short NS agency.techar.ch @1.1.1.1
```

Both should return the same four `*.awsdns-*.{com,net,org,co.uk}` nameservers.

> If ACM validation hangs >15 min — see "Manual delegation fallback" at the bottom and reach for the Ctrl-C dance.

## Step 3 — Verify SSM contract

```bash
aws-vault exec personal_iphone -- aws ssm get-parameters-by-path \
  --path /ai-website-agency --recursive --query "Parameters[].Name"
```

Expected entries:

```
/ai-website-agency/cert/wildcard_arn_eu_west_2
/ai-website-agency/cert/wildcard_arn_us_east_1
/ai-website-agency/cf/bff_preview_distribution_id
/ai-website-agency/cf/bff_production_distribution_id
/ai-website-agency/cf/preview_distribution_id
/ai-website-agency/cf/production_distribution_id
/ai-website-agency/route53/zone_id
/ai-website-agency/route53/zone_name
/ai-website-agency/s3/preview_bucket
/ai-website-agency/s3/production_bucket
```

## Step 4 — Hand off to CI

In the GitHub repo settings (`Settings → Secrets and variables → Actions`):

- `AWS_ROLE_ARN` ← `aws-vault exec personal_iphone -- terraform output -raw github_actions_role_arn` (run from `aws-setup/`)
- `E2E_TEST_USER` ← e.g. `e2e-tester`
- `E2E_TEST_PASS` ← a random strong password (saved in your password manager)

Or from the CLI:

```bash
cd aws-setup
ROLE_ARN=$(aws-vault exec personal_iphone -- terraform output -raw github_actions_role_arn)
gh secret set AWS_ROLE_ARN  --body "$ROLE_ARN"
gh secret set E2E_TEST_USER --body "e2e-tester"
gh secret set E2E_TEST_PASS --body "$(openssl rand -base64 24)"
```

## Step 5 — First production deploy

```bash
git checkout main
git push origin main
```

CI runs the full pipeline against `production`. Watch the Actions tab; deploy takes ~12 min cold cache.

When green, verify:

```bash
curl -fsS https://agency.techar.ch/
curl -fsS https://api.agency.techar.ch/health
curl -fsS https://bff.agency.techar.ch/health
```

All three should return.

## Step 6 — Verify per-branch envs work

```bash
git checkout -b feat-skeleton-test
git commit --allow-empty -m "test: verify per-branch env"
git push -u origin feat-skeleton-test
gh pr create --title "skeleton test" --body "verify per-branch env"
```

Watch the PR comment for the preview URL. When the pipeline is green:

```bash
curl -fsS https://preview.agency.techar.ch/feat-skeleton-test/
curl -fsS https://api-feat-skeleton-test.agency.techar.ch/health
curl -fsS https://feat-skeleton-test.bff.agency.techar.ch/health
```

Open `https://preview.agency.techar.ch/feat-skeleton-test/` in a browser; the React UI loads and renders the JSON from `/health`.

## Step 7 — Verify cleanup

```bash
gh pr close --delete-branch
```

The cleanup workflow runs (~10 min). When done, verify:

```bash
aws-vault exec personal_iphone -- aws apigatewayv2 get-domain-names \
  --query "Items[?contains(DomainName,'feat-skeleton-test')]"
aws-vault exec personal_iphone -- aws lambda list-functions \
  --query "Functions[?contains(FunctionName,'feat-skeleton-test')]"
aws-vault exec personal_iphone -- aws dynamodb list-tables \
  --query "TableNames[?contains(@,'feat-skeleton-test')]"
aws-vault exec personal_iphone -- aws s3 ls "s3://ai-website-agency-terraform-state-276447169330/terraform/ai-website-agency/feat-skeleton-test/"
aws-vault exec personal_iphone -- aws s3 ls "s3://ai-website-agency-frontend-preview-shared-276447169330/feat-skeleton-test/"
```

All five must be empty. If any leaks, see `scripts/cleanup-stale-envs.sh` (placeholder for ops use).

---

## Manual delegation fallback (cross-account / external-DNS parents)

If the parent zone `techar.ch` is moved out of this account or auto-delegation otherwise doesn't work, fall back to the original two-step ceremony:

1. Delete `aws-setup/route53-delegation.tf` (or comment it out).
2. First apply: `cd aws-setup && aws-vault exec personal_iphone -- terraform apply`. It will hang at `aws_acm_certificate_validation` because external resolvers can't see the validation CNAMEs yet. After ~5 min (certs visibly `PENDING_VALIDATION` in the console), `Ctrl-C` it.
3. Read the NS records: `aws-vault exec personal_iphone -- terraform output -raw delegation_instructions`.
4. Add the four NS records to wherever the parent zone lives (other AWS account, GoDaddy, Cloudflare, etc.) — name `ai-website-agency`, type `NS`, TTL 300, the four nameservers from step 3.
5. Verify: `dig +short NS agency.techar.ch @8.8.8.8` returns the four nameservers.
6. Re-run apply: `aws-vault exec personal_iphone -- terraform apply`. ACM validates → certs ISSUED → apply completes.
