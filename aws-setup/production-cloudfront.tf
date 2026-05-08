# Production frontend: agency.andrewreaassociates.com → S3 (website-agency-frontend-production) via OAC.
# NOT force_destroy — manual gate; this is the only prod frontend artifact.

resource "aws_s3_bucket" "frontend_production" {
  bucket        = "website-agency-frontend-production-${var.aws_account_id}"
  force_destroy = false

  tags = {
    Name      = "website-agency-frontend-production"
    Component = "frontend-prod"
  }
}

resource "aws_s3_bucket_versioning" "frontend_production" {
  bucket = aws_s3_bucket.frontend_production.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "frontend_production" {
  bucket = aws_s3_bucket.frontend_production.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "frontend_production" {
  bucket                  = aws_s3_bucket.frontend_production.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_cloudfront_origin_access_control" "frontend_production" {
  provider = aws.us_east_1

  name                              = "website-agency-frontend-production-oac"
  description                       = "OAC for website-agency production frontend"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

resource "aws_cloudfront_distribution" "frontend_production" {
  provider = aws.us_east_1

  enabled             = true
  is_ipv6_enabled     = true
  comment             = "website-agency production frontend (${var.base_domain})"
  default_root_object = "index.html"
  aliases             = [var.base_domain]
  price_class         = "PriceClass_100"
  web_acl_id          = aws_wafv2_web_acl.cloudfront.arn
  http_version        = "http2and3"

  origin {
    domain_name              = aws_s3_bucket.frontend_production.bucket_regional_domain_name
    origin_id                = "s3-frontend-production"
    origin_access_control_id = aws_cloudfront_origin_access_control.frontend_production.id
  }

  default_cache_behavior {
    target_origin_id           = "s3-frontend-production"
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["GET", "HEAD", "OPTIONS"]
    cached_methods             = ["GET", "HEAD"]
    compress                   = true
    cache_policy_id            = data.aws_cloudfront_cache_policy.caching_optimized.id
    response_headers_policy_id = aws_cloudfront_response_headers_policy.security_headers.id
  }

  # Long-cache the immutable Vite-hashed assets.
  ordered_cache_behavior {
    path_pattern               = "/assets/*"
    target_origin_id           = "s3-frontend-production"
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["GET", "HEAD", "OPTIONS"]
    cached_methods             = ["GET", "HEAD"]
    compress                   = true
    cache_policy_id            = data.aws_cloudfront_cache_policy.caching_optimized.id
    response_headers_policy_id = aws_cloudfront_response_headers_policy.security_headers.id
  }

  # SPA fallback — any 403/404 returns index.html so client-side routing works.
  custom_error_response {
    error_code            = 403
    response_code         = 200
    response_page_path    = "/index.html"
    error_caching_min_ttl = 0
  }
  custom_error_response {
    error_code            = 404
    response_code         = 200
    response_page_path    = "/index.html"
    error_caching_min_ttl = 0
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
    Name      = "website-agency-frontend-production"
    Component = "frontend-prod"
  }
}

# OAC bucket policy — allow CloudFront only.
resource "aws_s3_bucket_policy" "frontend_production_oac" {
  bucket = aws_s3_bucket.frontend_production.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid    = "AllowCloudFrontOAC"
      Effect = "Allow"
      Principal = {
        Service = "cloudfront.amazonaws.com"
      }
      Action   = "s3:GetObject"
      Resource = "${aws_s3_bucket.frontend_production.arn}/*"
      Condition = {
        StringEquals = {
          "AWS:SourceArn" = aws_cloudfront_distribution.frontend_production.arn
        }
      }
    }]
  })
}

# Route53 A-alias for the apex.
resource "aws_route53_record" "production_apex" {
  zone_id = aws_route53_zone.website-agency.zone_id
  name    = var.base_domain
  type    = "A"
  alias {
    name                   = aws_cloudfront_distribution.frontend_production.domain_name
    zone_id                = aws_cloudfront_distribution.frontend_production.hosted_zone_id
    evaluate_target_health = false
  }
}

# Managed CloudFront cache policies — fetched once and reused.
data "aws_cloudfront_cache_policy" "caching_optimized" {
  provider = aws.us_east_1
  name     = "Managed-CachingOptimized"
}

data "aws_cloudfront_cache_policy" "caching_disabled" {
  provider = aws.us_east_1
  name     = "Managed-CachingDisabled"
}

data "aws_cloudfront_origin_request_policy" "all_viewer_except_host" {
  provider = aws.us_east_1
  name     = "Managed-AllViewerExceptHostHeader"
}
