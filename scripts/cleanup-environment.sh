#!/usr/bin/env bash
# Empty all per-env S3 buckets (versioned + non-versioned, abort multipart) BEFORE
# `terraform destroy`. Without this, terraform destroy fails on non-empty buckets
# even with force_destroy=true if IAM lacks DeleteObjectVersion (pitfall #1 — smm
# 8c01880, tripwire 1db6f99).
#
# DENYLIST: refuses to run for production/main/master/prod/develop. Defense in
# depth — the cleanup workflow job has the same check as its first step.
#
# Usage: ENVIRONMENT=feat-x scripts/cleanup-environment.sh

set -euo pipefail

ENVIRONMENT="${ENVIRONMENT:?ENVIRONMENT must be set}"

case "${ENVIRONMENT}" in
  production|main|master|prod|develop)
    echo "::error::REFUSING to clean up protected environment '${ENVIRONMENT}'."
    exit 1 ;;
esac

echo "==> Cleaning up environment: ${ENVIRONMENT}"

# Anchored regex so we don't match `ai-website-agency-something-else` accidentally
# (smm pattern was loose — see pitfall #13).
PATTERN="^ai-website-agency-.*-${ENVIRONMENT}-[0-9]{12}$|^ai-website-agency-.*-${ENVIRONMENT}$"

BUCKETS=$(aws s3api list-buckets --query 'Buckets[].Name' --output text | tr '\t' '\n' | grep -E "${PATTERN}" || true)

if [[ -z "${BUCKETS}" ]]; then
  echo "No matching buckets found for env '${ENVIRONMENT}' (pattern: ${PATTERN})."
  echo "Skipping S3 cleanup."
  exit 0
fi

for BUCKET in ${BUCKETS}; do
  echo "----"
  echo "Bucket: ${BUCKET}"

  # Defense check — refuse to touch any bucket whose name doesn't include the env.
  if [[ "${BUCKET}" != *"${ENVIRONMENT}"* ]]; then
    echo "::error::Bucket ${BUCKET} doesn't match env ${ENVIRONMENT} — refusing."
    exit 1
  fi

  echo "  → Aborting incomplete multipart uploads"
  aws s3api list-multipart-uploads --bucket "${BUCKET}" --query 'Uploads[].[Key,UploadId]' --output text 2>/dev/null \
    | while IFS=$'\t' read -r KEY UPLOAD_ID; do
        [[ -z "${KEY}" ]] && continue
        aws s3api abort-multipart-upload --bucket "${BUCKET}" --key "${KEY}" --upload-id "${UPLOAD_ID}" || true
      done

  echo "  → Deleting current objects"
  aws s3 rm "s3://${BUCKET}" --recursive --quiet || true

  echo "  → Deleting all object versions and delete markers"
  while :; do
    PAGE=$(aws s3api list-object-versions --bucket "${BUCKET}" --max-items 1000 --output json 2>/dev/null || echo '{}')
    OBJECTS=$(echo "${PAGE}" | jq -c '
      [.Versions[]?, .DeleteMarkers[]? | { Key: .Key, VersionId: .VersionId }]
      | { Objects: . }
    ')
    COUNT=$(echo "${OBJECTS}" | jq '.Objects | length')
    if [[ "${COUNT}" == "0" ]]; then
      break
    fi
    echo "    deleting ${COUNT} versions/markers…"
    echo "${OBJECTS}" | aws s3api delete-objects --bucket "${BUCKET}" --delete file:///dev/stdin > /dev/null
  done

  echo "  ✓ Bucket ${BUCKET} emptied"
done

echo "==> S3 cleanup complete for env ${ENVIRONMENT}"
