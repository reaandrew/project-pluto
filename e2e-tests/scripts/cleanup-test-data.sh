#!/usr/bin/env bash
# Removes the e2e operator seeded by seed-test-user.sh. Best-effort:
# never fails the pipeline (the env is torn down anyway on PR close, and
# a synthetic prod run must not red the build on a cleanup hiccup).

set -uo pipefail

ENVIRONMENT="${ENVIRONMENT:?ENVIRONMENT must be set}"

# Skeleton pitfall #13 — denylist guard FIRST. CI never deletes from a
# protected/shared pool (seed-test-user.sh never created one there).
case "${ENVIRONMENT}" in
  production | main | master | prod | develop)
    echo "==> '${ENVIRONMENT}' is a protected env — skipping cleanup (skeleton pitfall #13)"
    exit 0
    ;;
esac

E2E_TEST_USER="${E2E_TEST_USER:-}"
AWS_REGION="${AWS_REGION:-eu-west-2}"

if [[ -z "${E2E_TEST_USER}" ]]; then
  echo "==> cleanup: E2E_TEST_USER unset, nothing to do"
  exit 0
fi

# Same sanitization as terraform local.env_sanitized (pitfall #8/#19).
ENV_SAN=$(printf '%s' "${ENVIRONMENT}" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9-]/-/g' | cut -c1-24)
POOL_NAME="ai-website-agency-${ENV_SAN}-operators"

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
