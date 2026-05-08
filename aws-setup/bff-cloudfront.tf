# Two BFF distributions:
#   - bff_production: bff.agency.techar.ch, fixed origin api.agency.techar.ch.
#     CloudFront Function on viewer-request transforms cookie → Authorization header.
#     No Lambda@Edge (origin is fixed).
#   - bff_preview: *.bff.agency.techar.ch, the same CloudFront Function PLUS a
#     Lambda@Edge on origin-request that reads x-original-host (stamped by the CFFn)
#     and rewrites the origin to api-<env>.agency.techar.ch.

# CloudFront Function — viewer-request handler.
resource "aws_cloudfront_function" "cookie_to_auth" {
  provider = aws.us_east_1

  name    = "ai-website-agency-cookie-to-auth"
  runtime = "cloudfront-js-2.0"
  publish = true
  code    = file("${path.module}/cloudfront-functions/ai-website-agency-cookie-to-auth.js")
  comment = "Transforms auth_token cookie to Authorization header; stamps x-original-host"
}

# Lambda@Edge for BFF preview origin rewriting.
data "archive_file" "bff_origin_router" {
  type        = "zip"
  source_file = "${path.module}/lambda-edge/ai-website-agency-bff-origin-router.js"
  output_path = "${path.module}/.terraform/ai-website-agency-bff-origin-router.zip"
}

resource "aws_lambda_function" "bff_origin_router" {
  provider = aws.us_east_1

  filename         = data.archive_file.bff_origin_router.output_path
  function_name    = "ai-website-agency-bff-origin-router"
  role             = aws_iam_role.lambda_edge_execution.arn
  handler          = "ai-website-agency-bff-origin-router.handler"
  source_code_hash = data.archive_file.bff_origin_router.output_base64sha256
  runtime          = "nodejs20.x"
  timeout          = 5
  memory_size      = 128
  publish          = true

  tags = {
    Name = "ai-website-agency-bff-origin-router"
  }
}

# ----- Production BFF -----
resource "aws_cloudfront_distribution" "bff_production" {
  provider = aws.us_east_1

  enabled         = true
  is_ipv6_enabled = true
  comment         = "ai-website-agency production BFF (bff.${var.base_domain})"
  aliases         = ["bff.${var.base_domain}"]
  price_class     = "PriceClass_100"
  web_acl_id      = aws_wafv2_web_acl.cloudfront.arn
  http_version    = "http2and3"

  origin {
    domain_name = "api.${var.base_domain}"
    origin_id   = "api-production"
    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "https-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }
  }

  default_cache_behavior {
    target_origin_id           = "api-production"
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods             = ["GET", "HEAD"]
    compress                   = true
    cache_policy_id            = data.aws_cloudfront_cache_policy.caching_disabled.id
    origin_request_policy_id   = data.aws_cloudfront_origin_request_policy.all_viewer_except_host.id
    response_headers_policy_id = aws_cloudfront_response_headers_policy.cors_passthrough.id

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.cookie_to_auth.arn
    }
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    acm_certificate_arn      = aws_acm_certificate_validation.wildcard_us_east_1.certificate_arn
    minimum_protocol_version = "TLSv1.2_2021"
    ssl_support_method       = "sni-only"
  }

  tags = {
    Name      = "ai-website-agency-bff-production"
    Component = "bff-prod"
  }
}

# ----- Preview BFF (wildcard) -----
resource "aws_cloudfront_distribution" "bff_preview" {
  provider = aws.us_east_1

  enabled         = true
  is_ipv6_enabled = true
  comment         = "ai-website-agency preview BFF (*.bff.${var.base_domain})"
  aliases         = ["*.bff.${var.base_domain}"]
  price_class     = "PriceClass_100"
  web_acl_id      = aws_wafv2_web_acl.cloudfront.arn
  http_version    = "http2and3"

  # Default origin is a placeholder — Lambda@Edge rewrites it per request.
  origin {
    domain_name = "api.${var.base_domain}"
    origin_id   = "api-placeholder"
    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "https-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }
  }

  default_cache_behavior {
    target_origin_id           = "api-placeholder"
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods             = ["GET", "HEAD"]
    compress                   = true
    cache_policy_id            = data.aws_cloudfront_cache_policy.caching_disabled.id
    origin_request_policy_id   = data.aws_cloudfront_origin_request_policy.all_viewer_except_host.id
    response_headers_policy_id = aws_cloudfront_response_headers_policy.cors_passthrough.id

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.cookie_to_auth.arn
    }

    lambda_function_association {
      event_type   = "origin-request"
      lambda_arn   = aws_lambda_function.bff_origin_router.qualified_arn
      include_body = false
    }
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    acm_certificate_arn      = aws_acm_certificate_validation.wildcard_us_east_1.certificate_arn
    minimum_protocol_version = "TLSv1.2_2021"
    ssl_support_method       = "sni-only"
  }

  tags = {
    Name      = "ai-website-agency-bff-preview"
    Component = "bff-preview"
  }
}

# Route53 records — apex + wildcard.
resource "aws_route53_record" "bff_production" {
  zone_id = aws_route53_zone.ai-website-agency.zone_id
  name    = "bff.${var.base_domain}"
  type    = "A"
  alias {
    name                   = aws_cloudfront_distribution.bff_production.domain_name
    zone_id                = aws_cloudfront_distribution.bff_production.hosted_zone_id
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "bff_preview_wildcard" {
  zone_id = aws_route53_zone.ai-website-agency.zone_id
  name    = "*.bff.${var.base_domain}"
  type    = "A"
  alias {
    name                   = aws_cloudfront_distribution.bff_preview.domain_name
    zone_id                = aws_cloudfront_distribution.bff_preview.hosted_zone_id
    evaluate_target_health = false
  }
}
