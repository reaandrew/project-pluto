# cloud-skeleton

A production-grade AWS cloud-native skeleton **GitHub template** for new projects.

Click **"Use this template"** at the top of this repo on GitHub, give your new repo a name, then run `bin/init.sh` and you have:

- Vite/React/TypeScript frontend
- Go Lambda on `provided.al2023`
- API Gateway v2 (HTTP API) with custom domain
- DynamoDB (PAY_PER_REQUEST, PITR + deletion-protection in prod only)
- BFF CloudFront with cookie → Authorization-header transform
- A **fully isolated cloud preview environment for every PR**, automatically destroyed on PR close
- A GitHub Actions pipeline gating every PR through lint + types + unit + secret-scan + SAST (Semgrep) + SCA + IaC (Checkov + tfsec) + Trivy + smoke + E2E (Playwright) + DAST (ZAP) + a11y (Lighthouse) + perf (k6) — all green, no advisory cope

## Use this template

```bash
# 1. On GitHub: click "Use this template" → "Create a new repository"
# 2. Clone your new repo
git clone https://github.com/YOUR-ORG/YOUR-REPO.git
cd YOUR-REPO

# 3. Customise — replaces every "ai-website-agency" / account / domain / org reference
bin/init.sh \
  --project        myapp \
  --account-id     123456789012 \
  --base-domain    myapp.example.com \
  --parent-domain  example.com \
  --github-org     YOUR-ORG \
  --github-repo    YOUR-REPO \
  --aws-vault-profile my-aws-profile

# 4. Push (init.sh re-initialises git history with a single commit)
git remote add origin https://github.com/YOUR-ORG/YOUR-REPO.git
git push -u origin main

# 5. Run the one-time AWS bootstrap (creates state bucket, hosted zone,
#    ACM certs, 4× CloudFront, OIDC role, etc.) — see docs/BOOTSTRAP.md
```

`bin/init.sh --dry-run ...` shows you what it would change without touching anything.

### What `init.sh` does (verified end-to-end)

- Replaces `ai-website-agency` → your project, `276447169330` → your account, `agency.techar.ch` → your domain, `reaandrew/ai-website-agency` → your org/repo, `personal_iphone` → your aws-vault profile, and rewrites the Go module path.
- Substitutes across every `.tf`, `.go`, `.yml`, `.md`, `.ts`, `.tsx`, `.js`, `.json`, `.sh`, `go.mod`, `package.json`, `package-lock.json`, `.gitignore` file in the repo (excluding `.git`, `node_modules`, `.terraform`, `frontend/dist`, the placeholder bootstrap binary, and `bin/init.sh` itself).
- Tested on a throwaway copy: 38/75 files substituted, **zero residual `ai-website-agency` / `levantar` / `276447169330` references** left in any source file.
- Re-initialises git history with a single `Initial commit from cloud-skeleton template` so each new project starts clean.
- Validates inputs: 12-digit AWS account id, sane project regex (`^[a-z][a-z0-9-]{1,30}$`), all required flags present.
- Prints the exact `gh repo create` + `gh secret set` + `aws-vault terraform apply` commands you should run next.

After running `init.sh`, you can delete `bin/init.sh` — it has done its job.

### Required GitHub secrets (after first AWS bootstrap)

```bash
gh secret set AWS_ROLE_ARN  --body "arn:aws:iam::<ACCOUNT_ID>:role/github-actions-<PROJECT>"
gh secret set E2E_TEST_USER --body "e2e-tester"
gh secret set E2E_TEST_PASS --body "$(openssl rand -base64 24)"
```

`AWS_ROLE_ARN` value comes from `terraform output -raw github_actions_role_arn` after the first `aws-setup/` apply. `init.sh` prints the exact command line you need.

## Per-environment URL contract

| | Production (`main`) | Preview (PR branch `feat-x`) |
|---|---|---|
| Frontend | `https://YOUR-DOMAIN/` | `https://preview.YOUR-DOMAIN/feat-x/` |
| BFF | `https://bff.YOUR-DOMAIN/` | `https://feat-x.bff.YOUR-DOMAIN/` |
| API | `https://api.YOUR-DOMAIN/` | `https://api-feat-x.YOUR-DOMAIN/` |

Per-branch envs are spawned automatically on PR open and destroyed on PR close. The two preview-side CloudFront distributions and the wildcard ACM cert are **shared singletons** in `aws-setup/` — never per-branch (the 15-min provision/destroy time killed the previous attempts at this).

## Repo layout

```
aws-setup/    — one-time bootstrap (zone, certs, CloudFronts, OIDC, IAM role)
terraform/    — per-env stack applied by CI (Lambda, API GW, DynamoDB, S3)
lambdas/      — Go Lambda source + shared pkg/
frontend/     — Vite React TS app
scripts/      — derive-env-name, wait-for-endpoint, deploy-frontend, cleanup-environment
e2e-tests/    — Playwright (runs directly on the runner, no Docker)
.github/      — GitHub Actions workflows (deploy.yml, destroy.yml, e2e-tests.yml, security.yml, ...)
docs/         — BOOTSTRAP.md (one-time setup runbook), ARCHITECTURE.md
bin/init.sh   — template customiser; delete after running
```

## Pitfall hall-of-fame (avoided here)

20 known footguns from prior attempts (S3 force_destroy IAM gaps, missing dummy bootstrap binaries at plan time, IAM 10KB inline policy limit, hardcoded domains, SSM overwrite races, stale tfstate after destroy, Docker pull flakiness, asset-path breakage under preview prefixes, per-branch CloudFront blow-up time, etc.) are mitigated up front. See `docs/ARCHITECTURE.md` for the full table.

## Provenance

Hardened by repeated production use (this is bootstrap attempt #6 across the techar.ch org). The reference implementation lives at `reaandrew/ai-website-agency`.
