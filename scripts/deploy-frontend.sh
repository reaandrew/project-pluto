#!/usr/bin/env bash
# Deploy frontend/dist/ to the per-env S3 path and invalidate CloudFront.
#
# Usage: deploy-frontend.sh prod
#        deploy-frontend.sh preview <env>
#
# Behaviour:
#   - assets/* → default cache headers (long TTL, content-hashed)
#   - *.html, runtime-config.js → no-cache (so re-deploys are visible immediately)
#   - per-env runtime-config.js is generated fresh and uploaded after the sync,
#     so the same dist/ artifact deploys to every preview env
#   - CloudFront invalidation with exponential backoff on flake (pitfall #16)

set -euo pipefail

MODE="${1:?usage: deploy-frontend.sh <prod|preview> [env]}"
ENV_NAME="${2:-production}"
DIST_DIR="${DIST_DIR:-frontend/dist}"
BASE_DOMAIN="${BASE_DOMAIN:-agency.andrewreaassociates.com}"

if [[ ! -d "${DIST_DIR}" ]]; then
  echo "::error::DIST_DIR ${DIST_DIR} does not exist; did the build job upload the artifact?"
  exit 1
fi

case "${MODE}" in
  prod)
    BUCKET=$(aws ssm get-parameter --name /website-agency/s3/production_bucket --query 'Parameter.Value' --output text)
    DIST_ID=$(aws ssm get-parameter --name /website-agency/cf/production_distribution_id --query 'Parameter.Value' --output text)
    S3_PREFIX="s3://${BUCKET}/"
    INVALIDATION_PATH="/*"
    BFF_URL="https://bff.${BASE_DOMAIN}"
    API_URL="https://api.${BASE_DOMAIN}"
    ENV_LABEL="production"
    ;;
  preview)
    if [[ -z "${ENV_NAME}" || "${ENV_NAME}" == "production" ]]; then
      echo "::error::preview mode requires an env name (and not 'production')"
      exit 1
    fi
    BUCKET=$(aws ssm get-parameter --name /website-agency/s3/preview_bucket --query 'Parameter.Value' --output text)
    DIST_ID=$(aws ssm get-parameter --name /website-agency/cf/preview_distribution_id --query 'Parameter.Value' --output text)
    S3_PREFIX="s3://${BUCKET}/${ENV_NAME}/"
    INVALIDATION_PATH="/${ENV_NAME}/*"
    BFF_URL="https://${ENV_NAME}.bff.${BASE_DOMAIN}"
    API_URL="https://api-${ENV_NAME}.${BASE_DOMAIN}"
    ENV_LABEL="${ENV_NAME}"
    ;;
  *)
    echo "::error::unknown mode ${MODE} — expected prod or preview"
    exit 1 ;;
esac

# Generate per-env runtime-config.js BEFORE the sync so it lands in the same place
# as index.html.
GIT_SHA="${GITHUB_SHA:-$(git -C "$(dirname "${DIST_DIR}")" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
cat > "${DIST_DIR}/runtime-config.js" <<EOF
window.__FINANCE_CONFIG__ = {
  bffBaseUrl: "${BFF_URL}",
  apiBaseUrl: "${API_URL}",
  environment: "${ENV_LABEL}",
  gitSha: "${GIT_SHA}",
};
EOF

echo "==> Sync assets (long-cache) to ${S3_PREFIX}"
aws s3 sync "${DIST_DIR}/" "${S3_PREFIX}" \
  --delete \
  --exclude "*.html" \
  --exclude "runtime-config.js"

echo "==> Upload no-cache files (HTML, runtime-config.js) to ${S3_PREFIX}"
find "${DIST_DIR}" -maxdepth 2 \( -name '*.html' -o -name 'runtime-config.js' \) -type f | while read -r f; do
  REL="${f#${DIST_DIR}/}"
  aws s3 cp "${f}" "${S3_PREFIX}${REL}" --cache-control "no-cache, no-store, must-revalidate"
done

echo "==> Invalidate CloudFront ${DIST_ID} paths ${INVALIDATION_PATH}"
for ATTEMPT in 1 2 3 4 5; do
  if INVID=$(aws cloudfront create-invalidation --distribution-id "${DIST_ID}" --paths "${INVALIDATION_PATH}" --query 'Invalidation.Id' --output text 2>&1); then
    echo "Invalidation ${INVID} submitted"
    break
  fi
  WAIT=$((1 << ATTEMPT)) # 2,4,8,16,32
  echo "::warning::CloudFront invalidation attempt ${ATTEMPT}/5 failed; sleeping ${WAIT}s"
  if [[ "${ATTEMPT}" == "5" ]]; then
    echo "::error::CloudFront invalidation gave up after 5 attempts"
    exit 1
  fi
  sleep "${WAIT}"
done

if [[ "${ENV_LABEL}" == "production" ]]; then
  FRONTEND_URL="https://${BASE_DOMAIN}"
else
  FRONTEND_URL="https://preview.${BASE_DOMAIN}/${ENV_NAME}/"
fi
echo "==> Done."
echo "    Frontend URL: ${FRONTEND_URL}"
echo "    BFF URL:      ${BFF_URL}"
echo "    API URL:      ${API_URL}"
