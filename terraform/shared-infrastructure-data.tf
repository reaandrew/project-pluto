# Reads the SSM contract published by aws-setup/. Every domain string in this
# stack flows from var.base_domain via these locals — no hardcoded literals
# anywhere else (pitfall #7 — tripwire e119c83 / 0b817e2).

data "aws_route53_zone" "main" {
  name         = var.base_domain
  private_zone = false
}

data "aws_ssm_parameter" "cert_eu_west_2" {
  name = "/website-agency/cert/wildcard_arn_eu_west_2"
}

data "aws_ssm_parameter" "production_cf_id" {
  count = local.is_production ? 1 : 0
  name  = "/website-agency/cf/production_distribution_id"
}

data "aws_ssm_parameter" "preview_cf_id" {
  count = local.is_production ? 0 : 1
  name  = "/website-agency/cf/preview_distribution_id"
}

data "aws_ssm_parameter" "production_bucket" {
  count = local.is_production ? 1 : 0
  name  = "/website-agency/s3/production_bucket"
}

data "aws_ssm_parameter" "preview_bucket" {
  count = local.is_production ? 0 : 1
  name  = "/website-agency/s3/preview_bucket"
}

locals {
  cloudfront_id = local.is_production ? data.aws_ssm_parameter.production_cf_id[0].value : data.aws_ssm_parameter.preview_cf_id[0].value
  s3_bucket     = local.is_production ? data.aws_ssm_parameter.production_bucket[0].value : data.aws_ssm_parameter.preview_bucket[0].value

  # Per-env URLs.
  api_domain   = local.is_production ? "api.${var.base_domain}" : "api-${local.env_sanitized}.${var.base_domain}"
  bff_domain   = local.is_production ? "bff.${var.base_domain}" : "${local.env_sanitized}.bff.${var.base_domain}"
  frontend_url = local.is_production ? "https://${var.base_domain}" : "https://preview.${var.base_domain}/${local.env_sanitized}/"

  # CI uses this as `aws s3 sync frontend/dist/ <s3_upload_path>/`.
  s3_upload_path = local.is_production ? "s3://${local.s3_bucket}/" : "s3://${local.s3_bucket}/${local.env_sanitized}/"
}
