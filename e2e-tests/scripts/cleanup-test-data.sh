#!/usr/bin/env bash
# Removes the e2e operator seeded by seed-test-user.sh. Best-effort:
# never fails the pipeline (the env is torn down anyway on PR close, and
# a synthetic prod run must not red the build on a cleanup hiccup).

set -uo pipefail

ENVIRONMENT="${ENVIRONMENT:?ENVIRONMENT must be set}"
E2E_TEST_USER="${E2E_TEST_USER:-}"
AWS_REGION="${AWS_REGION:-eu-west-2}"

if [[ -z "${E2E_TEST_USER}" ]]; then
  echo "==> cleanup: E2E_TEST_USER unset, nothing to do"
  exit 0
fi

if [[ "${ENVIRONMENT}" == "production" ]]; then
  POOL_NAME="ai-website-agency-operators"
else
  POOL_NAME="ai-website-agency-${ENVIRONMENT}-operators"
fi

POOL_ID=""
NEXT=""
while :; do
  if [[ -n "${NEXT}" ]]; then
    PAGE=$(aws cognito-idp list-user-pools --max-results 60 --region "${AWS_REGION}" --next-token "${NEXT}" 2>/dev/null) || break
  else
    PAGE=$(aws cognito-idp list-user-pools --max-results 60 --region "${AWS_REGION}" 2>/dev/null) || break
  fi
  POOL_ID=$(echo "${PAGE}" | jq -r --arg n "${POOL_NAME}" '.UserPools[] | select(.Name == $n) | .Id' | head -1)
  [[ -n "${POOL_ID}" ]] && break
  NEXT=$(echo "${PAGE}" | jq -r '.NextToken // empty')
  [[ -z "${NEXT}" ]] && break
done

if [[ -z "${POOL_ID}" ]]; then
  echo "==> cleanup: pool '${POOL_NAME}' not found (already torn down?) — nothing to do"
  exit 0
fi

if aws cognito-idp admin-delete-user \
  --user-pool-id "${POOL_ID}" \
  --username "${E2E_TEST_USER}" \
  --region "${AWS_REGION}" 2>/dev/null; then
  echo "==> cleanup: deleted ${E2E_TEST_USER} from ${POOL_NAME}"
else
  echo "==> cleanup: ${E2E_TEST_USER} not present in ${POOL_NAME} — nothing to do"
fi
exit 0
