#!/usr/bin/env bash
# Seeds (and self-heals) the e2e operator in the per-env Cognito pool so
# the headless-login auth spec can sign in through the real Hosted UI.
#
# Idempotent: re-running resets the password and group membership, so a
# half-created user from a previous run can't wedge the gate. Creates a
# CONFIRMED user (admin-set-user-password --permanent) so there is no
# FORCE_CHANGE_PASSWORD challenge in the browser flow.
#
# Runs in the e2e-tests.yml `seed-test-user` job, which has the deploy
# role (cognito-idp:*). No SSM contract for the pool id — it is resolved
# by the deterministic pool name (terraform: ai-website-agency<suffix>-operators).

set -euo pipefail

ENVIRONMENT="${ENVIRONMENT:?ENVIRONMENT must be set}"

# Skeleton pitfall #13 — denylist guard FIRST, before any side effect.
# This gate seeds a throwaway operator for per-PR preview envs only;
# the shared production/long-lived pools are never created into or
# deleted from by CI. Protected production auth is verified out-of-band.
case "${ENVIRONMENT}" in
  production | main | master | prod | develop)
    echo "==> '${ENVIRONMENT}' is a protected env — skipping operator seed (skeleton pitfall #13)"
    exit 0
    ;;
esac

E2E_TEST_USER="${E2E_TEST_USER:?E2E_TEST_USER must be set (repo/org secret)}"
E2E_TEST_PASS="${E2E_TEST_PASS:?E2E_TEST_PASS must be set (repo/org secret; must satisfy the 14-char upper/lower/number/symbol pool policy)}"
AWS_REGION="${AWS_REGION:-eu-west-2}"

# Pool name mirrors terraform/cognito.tf exactly:
#   name       = "ai-website-agency${local.env_suffix}-operators"
#   env_suffix = production ? "" : "-${local.env_sanitized}"
#   env_sanitized = substr(replace(lower(var.environment),"[^a-z0-9-]","-"),0,24)
# Apply the SAME sanitization here (skeleton pitfall #8/#19) so a raw
# workflow_dispatch value still resolves the real pool.
ENV_SAN=$(printf '%s' "${ENVIRONMENT}" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9-]/-/g' | cut -c1-24)
POOL_NAME="ai-website-agency-${ENV_SAN}-operators"
echo "==> seeding e2e operator into pool '${POOL_NAME}' (env=${ENVIRONMENT})"

# Resolve the pool id by name (paginate; an account can exceed one page).
POOL_ID=""
NEXT=""
while :; do
  if [[ -n "${NEXT}" ]]; then
    PAGE=$(aws cognito-idp list-user-pools --max-results 60 --region "${AWS_REGION}" --next-token "${NEXT}")
  else
    PAGE=$(aws cognito-idp list-user-pools --max-results 60 --region "${AWS_REGION}")
  fi
  POOL_ID=$(echo "${PAGE}" | jq -r --arg n "${POOL_NAME}" '.UserPools[] | select(.Name == $n) | .Id' | head -1)
  [[ -n "${POOL_ID}" ]] && break
  NEXT=$(echo "${PAGE}" | jq -r '.NextToken // empty')
  [[ -z "${NEXT}" ]] && break
done

if [[ -z "${POOL_ID}" ]]; then
  echo "ERROR: no Cognito user pool named '${POOL_NAME}' — is the env deployed?" >&2
  exit 1
fi
echo "    pool id: ${POOL_ID}"

# Create the user if absent (email is the username attribute). Tolerate
# an existing user — we reset its password + group below regardless.
if aws cognito-idp admin-create-user \
  --user-pool-id "${POOL_ID}" \
  --username "${E2E_TEST_USER}" \
  --message-action SUPPRESS \
  --user-attributes Name=email,Value="${E2E_TEST_USER}" Name=email_verified,Value=true \
  --region "${AWS_REGION}" >/dev/null 2>&1; then
  echo "    created ${E2E_TEST_USER}"
else
  echo "    ${E2E_TEST_USER} already exists — re-seeding"
fi

# Permanent password → CONFIRMED, no challenge in the Hosted UI.
aws cognito-idp admin-set-user-password \
  --user-pool-id "${POOL_ID}" \
  --username "${E2E_TEST_USER}" \
  --password "${E2E_TEST_PASS}" \
  --permanent \
  --region "${AWS_REGION}"
echo "    password set (permanent)"

# Operator group — the BFF gate requires the `operator` group claim.
aws cognito-idp admin-add-user-to-group \
  --user-pool-id "${POOL_ID}" \
  --username "${E2E_TEST_USER}" \
  --group-name operator \
  --region "${AWS_REGION}"
echo "    added to 'operator' group"
echo "==> seed complete"
