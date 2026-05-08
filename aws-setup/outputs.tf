output "github_actions_role_arn" {
  description = "ARN of the IAM role assumed by GitHub Actions via OIDC. Set this as the AWS_ROLE_ARN secret in the GitHub repo."
  value       = aws_iam_role.github_actions.arn
}

output "route53_zone_id" {
  value = aws_route53_zone.ai-website-agency.zone_id
}

output "route53_nameservers" {
  description = "Nameservers for agency.techar.ch. Add these as NS records in the parent techar.ch zone."
  value       = aws_route53_zone.ai-website-agency.name_servers
}

output "delegation_status" {
  description = "Status of the parent-zone delegation."
  value       = "AUTO — NS record for ${var.base_domain} added to parent zone ${var.parent_domain} (same AWS account) by aws_route53_record.delegation"
}

output "nameservers" {
  description = "The four nameservers for the new zone — already added to the parent zone automatically."
  value       = aws_route53_zone.ai-website-agency.name_servers
}

output "cert_arn_eu_west_2" {
  value = aws_acm_certificate_validation.wildcard_eu_west_2.certificate_arn
}

output "cert_arn_us_east_1" {
  value = aws_acm_certificate_validation.wildcard_us_east_1.certificate_arn
}

output "cloudfront_production_id" {
  value = aws_cloudfront_distribution.frontend_production.id
}

output "cloudfront_preview_id" {
  value = aws_cloudfront_distribution.frontend_preview.id
}

output "cloudfront_bff_production_id" {
  value = aws_cloudfront_distribution.bff_production.id
}

output "cloudfront_bff_preview_id" {
  value = aws_cloudfront_distribution.bff_preview.id
}

output "frontend_url_production" {
  value = "https://${var.base_domain}"
}

output "bff_url_production" {
  value = "https://bff.${var.base_domain}"
}

output "preview_url_template" {
  value = "https://preview.${var.base_domain}/<env>/"
}

output "preview_bff_url_template" {
  value = "https://<env>.bff.${var.base_domain}"
}

output "preview_api_url_template" {
  value = "https://api-<env>.${var.base_domain}"
}
