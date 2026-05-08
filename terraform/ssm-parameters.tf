# Pitfall #4 (tripwire 073ae18): never use `overwrite=true` for secrets — it nukes
# whatever the operator (or a previous run) wrote. Use `lifecycle.ignore_changes=[value]`
# and import the existing value with `terraform import` (CI does this in a loop).
#
# Skeleton has no real secrets yet; this resource exists as the canonical pattern
# for when they appear.

resource "aws_ssm_parameter" "example_app_secret" {
  name        = "/ai-website-agency/${var.environment}/secret/example_app_secret"
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  description = "Example secret. Set the real value via `aws ssm put-parameter --overwrite`; Terraform will not touch it again."

  lifecycle {
    ignore_changes = [value]
  }

  tags = local.common_tags
}
