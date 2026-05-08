# Two ACM certificates with identical SANs:
#   - eu-west-2 cert is consumed by API Gateway v2 custom domains (REGIONAL endpoint)
#   - us-east-1 cert is consumed by CloudFront (must live in us-east-1)
# Both cover the apex, *.agency.andrewreaassociates.com, and *.bff.agency.andrewreaassociates.com
# so per-branch envs get api-<env>.… and <env>.bff.… for free.

locals {
  cert_san = [
    var.base_domain,            # agency.andrewreaassociates.com
    "*.bff.${var.base_domain}", # <env>.bff.agency.andrewreaassociates.com
  ]
}

# ----- eu-west-2 (API Gateway) -----
resource "aws_acm_certificate" "wildcard_eu_west_2" {
  domain_name               = "*.${var.base_domain}"
  subject_alternative_names = local.cert_san
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }

  tags = {
    Name = "wildcard-${var.base_domain}-eu-west-2"
  }
}

resource "aws_route53_record" "cert_validation_eu_west_2" {
  for_each = {
    for dvo in aws_acm_certificate.wildcard_eu_west_2.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      type   = dvo.resource_record_type
      record = dvo.resource_record_value
    }
  }

  zone_id         = aws_route53_zone.ai-website-agency.zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "wildcard_eu_west_2" {
  certificate_arn         = aws_acm_certificate.wildcard_eu_west_2.arn
  validation_record_fqdns = [for r in aws_route53_record.cert_validation_eu_west_2 : r.fqdn]

  timeouts {
    create = "30m"
  }
}

# ----- us-east-1 (CloudFront) -----
resource "aws_acm_certificate" "wildcard_us_east_1" {
  provider = aws.us_east_1

  domain_name               = "*.${var.base_domain}"
  subject_alternative_names = local.cert_san
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }

  tags = {
    Name = "wildcard-${var.base_domain}-us-east-1"
  }
}

resource "aws_route53_record" "cert_validation_us_east_1" {
  for_each = {
    for dvo in aws_acm_certificate.wildcard_us_east_1.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      type   = dvo.resource_record_type
      record = dvo.resource_record_value
    }
  }

  zone_id         = aws_route53_zone.ai-website-agency.zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "wildcard_us_east_1" {
  provider = aws.us_east_1

  certificate_arn         = aws_acm_certificate.wildcard_us_east_1.arn
  validation_record_fqdns = [for r in aws_route53_record.cert_validation_us_east_1 : r.fqdn]

  timeouts {
    create = "30m"
  }
}
