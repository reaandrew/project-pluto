#!/usr/bin/env bash
# Single source of truth for branch → environment name.
# Sourced by every workflow step that needs ENVIRONMENT/IS_MAIN.
#
# Sets in $GITHUB_ENV (workflow scope) and, when called from a step that has
# $GITHUB_OUTPUT, also emits step outputs.
#
#   - main branch       → ENVIRONMENT=production, IS_MAIN=true
#   - any other branch  → ENVIRONMENT=<sanitised branch name>, IS_MAIN=false
#
# Branch name is lowercased, '/' → '-', and capped at **24 chars**.
#
# The cap binds on the tightest per-env resource name. Constraints:
#   - IAM role name: 64 char limit. Pattern `ai-website-agency-lambda-api-<env>` (29 + env)
#     → env ≤ 35. (Pitfall #8.)
#   - S3 bucket name: 63 char limit. Pattern
#     `ai-website-agency-uploads-<env>-<acct>` (26 + env + 13) = 39 + env → env ≤ 24.
#     The S3 constraint is the binding one for project name `ai-website-agency` (17
#     chars). The cloud-skeleton template's default project name `finance` (7 chars)
#     left ~10 chars more headroom and capped at 31 — that no longer fits this project.
# Same regex constraint applies in terraform/env-suffix.tf and the BFF Lambda@Edge
# router; do not change one without the others.
#
# Usage in a workflow:
#   - run: source ./scripts/derive-env-name.sh

set -euo pipefail

# GITHUB_HEAD_REF is set on EVERY pull_request event (including closed/merged) to the
# source branch name. We check it first so cleanup-on-PR-close always resolves to the
# branch env (e.g. feat-skeleton-test), never to "production". The previous logic
# checked GITHUB_REF first, but for PR-closed-after-merge events GITHUB_REF can be
# refs/heads/main (the merge destination), which silently routed cleanup at production.
if [[ -n "${GITHUB_HEAD_REF:-}" ]]; then
  RAW="${GITHUB_HEAD_REF}"
  ENVIRONMENT=$(echo "${RAW}" | tr '/' '-' | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9-]/-/g' | cut -c1-24)
  IS_MAIN="false"
elif [[ "${GITHUB_REF:-}" == "refs/heads/main" ]]; then
  ENVIRONMENT="production"
  IS_MAIN="true"
else
  RAW="${GITHUB_REF_NAME:-${1:-}}"
  if [[ -z "${RAW}" ]]; then
    echo "::error::Cannot derive environment — no GITHUB_HEAD_REF, GITHUB_REF_NAME, or argument" >&2
    exit 1
  fi
  ENVIRONMENT=$(echo "${RAW}" | tr '/' '-' | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9-]/-/g' | cut -c1-24)
  IS_MAIN="false"
fi

if [[ -n "${GITHUB_ENV:-}" ]]; then
  {
    echo "ENVIRONMENT=${ENVIRONMENT}"
    echo "IS_MAIN=${IS_MAIN}"
  } >> "${GITHUB_ENV}"
fi

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "environment=${ENVIRONMENT}"
    echo "is_main=${IS_MAIN}"
  } >> "${GITHUB_OUTPUT}"
fi

# Always export so downstream commands in the same step can use them.
export ENVIRONMENT
export IS_MAIN

echo "Resolved env: ${ENVIRONMENT} (main=${IS_MAIN})"
