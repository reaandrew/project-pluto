# Pitfall #8: long branch names (e.g. `feat/some-very-long-feature-branch-name`) blow
# past IAM role-name limits and other resource-name limits. Cap at 31 chars.
# Mirrors tripwire/terraform/env-suffix.tf and the same regex in
# scripts/derive-env-name.sh and the BFF Lambda@Edge router.

locals {
  env_sanitized = substr(replace(lower(var.environment), "/[^a-z0-9-]/", "-"), 0, 31)
  env_suffix    = var.environment == "production" ? "" : "-${local.env_sanitized}"
  is_production = var.environment == "production"

  common_tags = {
    Project     = "website-agency"
    Environment = var.environment
    ManagedBy   = "terraform"
  }
}
