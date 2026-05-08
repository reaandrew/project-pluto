# Single source of truth for the contract between aws-setup/ and terraform/.
# terraform/ reads these via `data "aws_ssm_parameter"` — never via cross-stack
# remote-state, never via hardcoded values.
#
# Adding a new entry here requires a corresponding `data` source in
# terraform/shared-infrastructure-data.tf.

resource "aws_ssm_parameter" "cert_eu_west_2" {
  name        = "/ai-website-agency/cert/wildcard_arn_eu_west_2"
  type        = "String"
  value       = aws_acm_certificate_validation.wildcard_eu_west_2.certificate_arn
  description = "ACM wildcard cert for *.${var.base_domain} in eu-west-2 (API Gateway)"
  tags        = { Name = "/ai-website-agency/cert/wildcard_arn_eu_west_2" }
}

resource "aws_ssm_parameter" "cert_us_east_1" {
  name        = "/ai-website-agency/cert/wildcard_arn_us_east_1"
  type        = "String"
  value       = aws_acm_certificate_validation.wildcard_us_east_1.certificate_arn
  description = "ACM wildcard cert for *.${var.base_domain} in us-east-1 (CloudFront)"
  tags        = { Name = "/ai-website-agency/cert/wildcard_arn_us_east_1" }
}

resource "aws_ssm_parameter" "cf_production" {
  name        = "/ai-website-agency/cf/production_distribution_id"
  type        = "String"
  value       = aws_cloudfront_distribution.frontend_production.id
  description = "CloudFront distribution id for production frontend"
  tags        = { Name = "/ai-website-agency/cf/production_distribution_id" }
}

resource "aws_ssm_parameter" "cf_preview" {
  name        = "/ai-website-agency/cf/preview_distribution_id"
  type        = "String"
  value       = aws_cloudfront_distribution.frontend_preview.id
  description = "CloudFront distribution id for preview frontend (shared across all branches)"
  tags        = { Name = "/ai-website-agency/cf/preview_distribution_id" }
}

resource "aws_ssm_parameter" "cf_bff_production" {
  name        = "/ai-website-agency/cf/bff_production_distribution_id"
  type        = "String"
  value       = aws_cloudfront_distribution.bff_production.id
  description = "CloudFront distribution id for production BFF"
  tags        = { Name = "/ai-website-agency/cf/bff_production_distribution_id" }
}

resource "aws_ssm_parameter" "cf_bff_preview" {
  name        = "/ai-website-agency/cf/bff_preview_distribution_id"
  type        = "String"
  value       = aws_cloudfront_distribution.bff_preview.id
  description = "CloudFront distribution id for preview BFF (wildcard, shared across all branches)"
  tags        = { Name = "/ai-website-agency/cf/bff_preview_distribution_id" }
}

resource "aws_ssm_parameter" "s3_production" {
  name        = "/ai-website-agency/s3/production_bucket"
  type        = "String"
  value       = aws_s3_bucket.frontend_production.id
  description = "S3 bucket name for production frontend"
  tags        = { Name = "/ai-website-agency/s3/production_bucket" }
}

resource "aws_ssm_parameter" "s3_preview" {
  name        = "/ai-website-agency/s3/preview_bucket"
  type        = "String"
  value       = aws_s3_bucket.frontend_preview_shared.id
  description = "S3 bucket name for preview frontend (shared across all branches)"
  tags        = { Name = "/ai-website-agency/s3/preview_bucket" }
}

resource "aws_ssm_parameter" "route53_zone_id" {
  name        = "/ai-website-agency/route53/zone_id"
  type        = "String"
  value       = aws_route53_zone.ai-website-agency.zone_id
  description = "Route53 zone id for ${var.base_domain}"
  tags        = { Name = "/ai-website-agency/route53/zone_id" }
}

resource "aws_ssm_parameter" "route53_zone_name" {
  name        = "/ai-website-agency/route53/zone_name"
  type        = "String"
  value       = var.base_domain
  description = "Route53 zone name for ${var.base_domain}"
  tags        = { Name = "/ai-website-agency/route53/zone_name" }
}
