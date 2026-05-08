#!/usr/bin/env bash
# Placeholder — seeds the e2e test user when auth lands.
# Same shape as scripts/seed-environment-users.sh; this one is invoked by the
# e2e-tests.yml workflow specifically (separate so e2e-tests.yml can be called
# standalone via workflow_dispatch).

set -euo pipefail

ENVIRONMENT="${ENVIRONMENT:?ENVIRONMENT must be set}"
E2E_TEST_USER="${E2E_TEST_USER:?E2E_TEST_USER must be set}"

echo "==> seed-test-user.sh placeholder (env=${ENVIRONMENT}, user=${E2E_TEST_USER})"
echo "    no-op for skeleton; replace when auth is implemented"
