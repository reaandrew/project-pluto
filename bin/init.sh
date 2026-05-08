#!/usr/bin/env bash
# Customise a fresh clone of the cloud-skeleton template for your project.
#
# Usage:
#   bin/init.sh \
#     --project        myapp \
#     --account-id     123456789012 \
#     --base-domain    myapp.example.com \
#     --parent-domain  example.com \
#     --github-org     my-org \
#     --github-repo    myapp \
#     --aws-vault-profile my-aws-profile \
#     [--dry-run]
#
# Substitutes every occurrence of the template defaults across .tf, .yml, .md,
# .go, .ts, .tsx, .js, .json, .sh, go.mod, .gitignore — then re-initialises the
# git history with a single "Initial commit from cloud-skeleton template".
#
# Run this ONCE, immediately after creating a fresh repo from the template.

set -euo pipefail

# ----- defaults you replace -----
DEFAULT_PROJECT="finance"
DEFAULT_ACCOUNT="134570442530"
DEFAULT_BASE_DOMAIN="finance.levantar.ai"
DEFAULT_PARENT_DOMAIN="levantar.ai"
DEFAULT_GITHUB_ORG="levantar-ai"
DEFAULT_GITHUB_REPO="finance"
DEFAULT_AWS_VAULT_PROFILE="lev:andy.rea"
DEFAULT_GOMOD_PATH="github.com/levantar-ai/finance"

PROJECT=""
ACCOUNT=""
BASE_DOMAIN=""
PARENT_DOMAIN=""
GITHUB_ORG=""
GITHUB_REPO=""
AWS_VAULT_PROFILE=""
DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project)            PROJECT="$2"; shift 2 ;;
    --account-id)         ACCOUNT="$2"; shift 2 ;;
    --base-domain)        BASE_DOMAIN="$2"; shift 2 ;;
    --parent-domain)      PARENT_DOMAIN="$2"; shift 2 ;;
    --github-org)         GITHUB_ORG="$2"; shift 2 ;;
    --github-repo)        GITHUB_REPO="$2"; shift 2 ;;
    --aws-vault-profile)  AWS_VAULT_PROFILE="$2"; shift 2 ;;
    --dry-run)            DRY_RUN=1; shift ;;
    -h|--help)            sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; exit 1 ;;
  esac
done

require() {
  local v="$1" n="$2"
  if [[ -z "$v" ]]; then
    echo "::error::missing required arg --$n" >&2
    exit 1
  fi
}
require "$PROJECT" project
require "$ACCOUNT" account-id
require "$BASE_DOMAIN" base-domain
require "$PARENT_DOMAIN" parent-domain
require "$GITHUB_ORG" github-org
require "$GITHUB_REPO" github-repo
require "$AWS_VAULT_PROFILE" aws-vault-profile

[[ ! "$ACCOUNT" =~ ^[0-9]{12}$ ]] && { echo "::error::--account-id must be 12 digits" >&2; exit 1; }
[[ ! "$PROJECT" =~ ^[a-z][a-z0-9-]{1,30}$ ]] && { echo "::error::--project must match ^[a-z][a-z0-9-]{1,30}\$" >&2; exit 1; }

GOMOD_PATH="github.com/${GITHUB_ORG}/${GITHUB_REPO}"

cat <<EOF

================================================================================
 Customising cloud-skeleton template
================================================================================
  project              ${DEFAULT_PROJECT}            -> ${PROJECT}
  account-id           ${DEFAULT_ACCOUNT}     -> ${ACCOUNT}
  base-domain          ${DEFAULT_BASE_DOMAIN}  -> ${BASE_DOMAIN}
  parent-domain        ${DEFAULT_PARENT_DOMAIN}         -> ${PARENT_DOMAIN}
  github-org           ${DEFAULT_GITHUB_ORG}          -> ${GITHUB_ORG}
  github-repo          ${DEFAULT_GITHUB_REPO}              -> ${GITHUB_REPO}
  aws-vault-profile    ${DEFAULT_AWS_VAULT_PROFILE}        -> ${AWS_VAULT_PROFILE}
  go module path       ${DEFAULT_GOMOD_PATH} -> ${GOMOD_PATH}
EOF
[[ "$DRY_RUN" == "1" ]] && echo "  (dry run — no files will be written)"
echo "================================================================================"
echo

# Files to substitute. Cast wide on purpose — substitution is exact-string only,
# so false positives are very unlikely. Excludes binary/state.
FIND_ARGS=(
  -type f
  -not -path './.git/*'
  -not -path './node_modules/*'
  -not -path './*/node_modules/*'
  -not -path './*/.terraform/*'
  -not -path './frontend/dist/*'
  -not -path './lambdas/*/bootstrap'  # placeholder is fine
  -not -path './bin/init.sh'           # don't rewrite this script
  -not -name '*.lock.hcl'
)

mapfile -t FILES < <(find . "${FIND_ARGS[@]}")

apply_subs() {
  local file="$1"
  if [[ "$DRY_RUN" == "1" ]]; then
    if grep -lE "${DEFAULT_PROJECT}|${DEFAULT_ACCOUNT}|${DEFAULT_BASE_DOMAIN}|${DEFAULT_GITHUB_ORG}/${DEFAULT_GITHUB_REPO}|${DEFAULT_AWS_VAULT_PROFILE}" "$file" >/dev/null 2>&1; then
      echo "  would edit $file"
    fi
    return 0
  fi
  # Order matters: do the longest/most specific replacements first to avoid
  # partial-match collisions.
  sed -i \
    -e "s|${DEFAULT_GOMOD_PATH}|${GOMOD_PATH}|g" \
    -e "s|${DEFAULT_GITHUB_ORG}/${DEFAULT_GITHUB_REPO}|${GITHUB_ORG}/${GITHUB_REPO}|g" \
    -e "s|${DEFAULT_BASE_DOMAIN}|${BASE_DOMAIN}|g" \
    -e "s|${DEFAULT_PARENT_DOMAIN}|${PARENT_DOMAIN}|g" \
    -e "s|${DEFAULT_AWS_VAULT_PROFILE}|${AWS_VAULT_PROFILE}|g" \
    -e "s|${DEFAULT_ACCOUNT}|${ACCOUNT}|g" \
    -e "s|${DEFAULT_GITHUB_ORG}|${GITHUB_ORG}|g" \
    -e "s|${DEFAULT_PROJECT}|${PROJECT}|g" \
    "$file"
}

count=0
for f in "${FILES[@]}"; do
  apply_subs "$f"
  count=$((count + 1))
done

echo "Processed ${count} files."

if [[ "$DRY_RUN" == "1" ]]; then
  echo "Dry run complete — no changes written."
  exit 0
fi

# Re-init git history so the new project starts from a single clean commit.
if [[ -d .git ]]; then
  rm -rf .git
fi
git init -q -b main
git add -A
git -c commit.gpgsign=false commit -q -m "Initial commit from cloud-skeleton template

Project:        ${PROJECT}
AWS account:    ${ACCOUNT}
Domain:         ${BASE_DOMAIN}
GitHub:         ${GITHUB_ORG}/${GITHUB_REPO}"

cat <<EOF

================================================================================
 Done. Next steps:
================================================================================

 1. Push to GitHub:
      gh repo create ${GITHUB_ORG}/${GITHUB_REPO} --private --source=. --remote=origin --push

 2. Set the AWS Role ARN secret (after running aws-setup once and capturing
    the github_actions_role_arn output):
      gh secret set AWS_ROLE_ARN --body "arn:aws:iam::${ACCOUNT}:role/github-actions-${PROJECT}"
      gh secret set E2E_TEST_USER --body "e2e-tester"
      gh secret set E2E_TEST_PASS --body "\$(openssl rand -base64 24)"

 3. Run the bootstrap (see docs/BOOTSTRAP.md):
      cd aws-setup
      aws-vault exec ${AWS_VAULT_PROFILE} -- terraform init
      aws-vault exec ${AWS_VAULT_PROFILE} -- terraform apply

 4. Push main — CI deploys production.
================================================================================
EOF
