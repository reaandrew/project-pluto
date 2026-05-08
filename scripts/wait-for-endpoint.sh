#!/usr/bin/env bash
# Poll an HTTP(S) endpoint until it returns the expected status code.
# Returns 0 on success, non-zero on timeout (so smoke-tests genuinely gate E2E —
# tripwire's smoke prints WARN and continues, which let bad deploys reach E2E).
#
# Usage: wait-for-endpoint.sh <url> [expected_status=200] [max_attempts=30] [sleep_seconds=10]

set -euo pipefail

URL="${1:?usage: wait-for-endpoint.sh <url> [expected_status] [max_attempts] [sleep_seconds]}"
EXPECTED="${2:-200}"
MAX_ATTEMPTS="${3:-30}"
SLEEP="${4:-10}"

echo "Waiting for ${URL} → HTTP ${EXPECTED} (max ${MAX_ATTEMPTS} attempts × ${SLEEP}s)"

for ATTEMPT in $(seq 1 "${MAX_ATTEMPTS}"); do
  CODE=$(curl -ksSL --max-time 10 -o /dev/null -w '%{http_code}' "${URL}" 2>/dev/null || echo "000")
  if [[ "${CODE}" == "${EXPECTED}" ]]; then
    echo "OK   attempt ${ATTEMPT}: ${URL} → ${CODE}"
    exit 0
  fi
  echo "WAIT attempt ${ATTEMPT}/${MAX_ATTEMPTS}: ${URL} → ${CODE}"
  sleep "${SLEEP}"
done

echo "::error::Timed out after ${MAX_ATTEMPTS} attempts waiting for ${URL} → ${EXPECTED}"
exit 1
