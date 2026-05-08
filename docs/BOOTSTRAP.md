# Bootstrap Runbook

One-time procedure to bring up the ai-website-agency project. Do this once per AWS account.

## Pre-flight

```bash
aws-vault exec personal_iphone -- aws sts get-caller-identity
# Expected:
#   "Account": "276447169330"
#   "Arn": "arn:aws:iam::276447169330:user/andy.rea"
```

Confirm the parent zone `andrewreaassociates.com` exists somewhere you can edit (AWS Route53 in some account, or external DNS provider). You will need to add four NS records to it later.

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

## Step 2 — First `aws-setup/` apply (will hang at cert validation; expected)

```bash
cd aws-setup
aws-vault exec personal_iphone -- terraform init
aws-vault exec personal_iphone -- terraform apply
```

The hosted zone is created, NS records emerge, ACM certs go to `PENDING_VALIDATION`. The `aws_acm_certificate_validation` resources hang because external resolvers can't see the validation CNAMEs yet — the parent zone hasn't been told to delegate.

After ~5 minutes (when the certs are visibly `PENDING_VALIDATION` in the AWS console), `Ctrl-C` the apply.

## Step 3 — Read the NS records

```bash
aws-vault exec personal_iphone -- terraform output -raw delegation_instructions
```

Output looks like:

```
===================================================================
ACTION REQUIRED: Add the following NS records to the parent zone
andrewreaassociates.com for the subdomain ai-website-agency:

  Name:   ai-website-agency
  Type:   NS
  TTL:    300
  Values:
    ns-123.awsdns-12.com
    ns-456.awsdns-34.net
    ns-789.awsdns-56.org
    ns-012.awsdns-78.co.uk
===================================================================
```

## Step 4 — Add NS records to the parent `andrewreaassociates.com` zone

In whichever AWS account / DNS provider hosts `andrewreaassociates.com`:

| Field | Value |
|---|---|
| Name | `ai-website-agency` (becomes `agency.andrewreaassociates.com`) |
| Type | `NS` |
| TTL | 300 |
| Values | the four nameservers from Step 3 |

## Step 5 — Verify delegation

```bash
dig +short NS agency.andrewreaassociates.com @8.8.8.8
dig +short NS agency.andrewreaassociates.com @1.1.1.1
```

Both must return the four nameservers from Step 3. Usually 1–5 minutes after adding the records.

## Step 6 — Re-run `aws-setup/` apply

```bash
cd aws-setup
aws-vault exec personal_iphone -- terraform apply
```

This time `aws_acm_certificate_validation` can resolve the validation CNAMEs and ACM transitions to `ISSUED`. Apply completes; SSM params written; CloudFront distributions created (full propagation lags 15–30 min — `terraform apply` returns when AWS accepts the config).

## Step 7 — Verify SSM contract

```bash
aws-vault exec personal_iphone -- aws ssm get-parameters-by-path --path /ai-website-agency --recursive --query "Parameters[].Name"
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

## Step 8 — Hand off to CI

In the GitHub repo settings (`Settings → Secrets and variables → Actions`):

- `AWS_ROLE_ARN` ← `aws-vault exec personal_iphone -- terraform output -raw github_actions_role_arn` (run from `aws-setup/`)
- `E2E_TEST_USER` ← e.g. `e2e-tester`
- `E2E_TEST_PASS` ← a random strong password (saved in your password manager)

## Step 9 — First production deploy

```bash
git checkout main
git push origin main
```

CI runs the full pipeline against `production`. Watch the Actions tab; deploy takes ~12 min cold cache.

When green, verify:

```bash
curl -fsS https://agency.andrewreaassociates.com/
curl -fsS https://api.agency.andrewreaassociates.com/health
curl -fsS https://bff.agency.andrewreaassociates.com/health
```

All three should return.

## Step 10 — Verify per-branch envs work

```bash
git checkout -b feat-skeleton-test
git commit --allow-empty -m "test: verify per-branch env"
git push -u origin feat-skeleton-test
gh pr create --title "skeleton test" --body "verify per-branch env"
```

Watch the PR comment for the preview URL. When the pipeline is green:

```bash
curl -fsS https://preview.agency.andrewreaassociates.com/feat-skeleton-test/
curl -fsS https://api-feat-skeleton-test.agency.andrewreaassociates.com/health
curl -fsS https://feat-skeleton-test.bff.agency.andrewreaassociates.com/health
```

Open `https://preview.agency.andrewreaassociates.com/feat-skeleton-test/` in a browser; the React UI loads and renders the JSON from `/health`.

## Step 11 — Verify cleanup

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
