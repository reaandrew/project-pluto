#!/usr/bin/env bash
# Placeholder — seeds the e2e test user when auth lands.
# For now: prints what it WOULD do so the workflow doesn't fail.
#
# Future shape:
#   - bcrypt-hash $E2E_TEST_PASS
#   - aws dynamodb put-item into website-agency-users-${ENVIRONMENT}
#   - mark user verified, set tier=free

set -euo pipefail

ENVIRONMENT="${ENVIRONMENT:?ENVIRONMENT must be set}"
TABLE="website-agency-users-${ENVIRONMENT}"
[[ "${ENVIRONMENT}" == "production" ]] && TABLE="website-agency-users"

echo "==> seed-environment-users.sh placeholder"
echo "    env:  ${ENVIRONMENT}"
echo "    user: ${E2E_TEST_USER:-<unset>}"
echo "    table (would write to): ${TABLE}"
echo "    no-op for skeleton; replace when auth is implemented"
