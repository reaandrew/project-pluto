# Pitfall #8: long branch names (e.g. `feat/some-very-long-feature-branch-name`) blow
# past per-env resource-name limits. Cap at 24 chars — bound by the S3 bucket-name
# 63-char limit applied to the `ai-website-agency-uploads-<env>-<acct>` pattern.
# Mirrors scripts/derive-env-name.sh and the same regex in the BFF Lambda@Edge router.

locals {
  env_sanitized = substr(replace(lower(var.environment), "/[^a-z0-9-]/", "-"), 0, 24)
  env_suffix    = var.environment == "production" ? "" : "-${local.env_sanitized}"
  is_production = var.environment == "production"

  common_tags = {
    Project     = "ai-website-agency"
    Environment = var.environment
    ManagedBy   = "terraform"
  }
}
