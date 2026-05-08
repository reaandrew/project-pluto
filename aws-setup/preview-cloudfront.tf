# Preview frontend — preview.agency.andrewreaassociates.com → SHARED S3 with path-prefix routing.
# Lambda@Edge inspects the request URI and routes /<env>/<path> to S3 path /<env>/<path>.
# This means PR open/close never touches CloudFront — only S3 sync + Route53 stays unchanged.

resource "aws_s3_bucket" "frontend_preview_shared" {
  bucket        = "ai-website-agency-frontend-preview-shared-${var.aws_account_id}"
  force_destroy = true # pitfall #1 — preview content is disposable

  tags = {
    Name      = "ai-website-agency-frontend-preview-shared"
    Component = "frontend-preview"
  }
}

resource "aws_s3_bucket_versioning" "frontend_preview_shared" {
  bucket = aws_s3_bucket.frontend_preview_shared.id
  versioning_configuration {
    status = "Suspended"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "frontend_preview_shared" {
  bucket = aws_s3_bucket.frontend_preview_shared.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "frontend_preview_shared" {
  bucket                  = aws_s3_bucket.frontend_preview_shared.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Lambda@Edge needs OAI (not OAC) because it sets `authMethod: 'origin-access-identity'`
# when rewriting the origin in origin-request handlers.
resource "aws_cloudfront_origin_access_identity" "frontend_preview_shared" {
  provider = aws.us_east_1

  comment = "OAI for ai-website-agency preview-shared bucket"
}

# IAM role for Lambda@Edge — must trust both lambda.amazonaws.com AND edgelambda.amazonaws.com.
resource "aws_iam_role" "lambda_edge_execution" {
  name        = "ai-website-agency-lambda-edge-execution"
  description = "Execution role for ai-website-agency Lambda@Edge functions"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Service = ["lambda.amazonaws.com", "edgelambda.amazonaws.com"]
      }
      Action = "sts:AssumeRole"
    }]
  })

  tags = {
    Name = "ai-website-agency-lambda-edge-execution"
  }
}

resource "aws_iam_role_policy_attachment" "lambda_edge_basic" {
  role       = aws_iam_role.lambda_edge_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

# Lambda@Edge for preview path-prefix routing.
data "archive_file" "preview_router" {
  type        = "zip"
  source_file = "${path.module}/lambda-edge/ai-website-agency-preview-router.js"
  output_path = "${path.module}/.terraform/ai-website-agency-preview-router.zip"
}

resource "aws_lambda_function" "preview_router" {
  provider = aws.us_east_1

  filename         = data.archive_file.preview_router.output_path
  function_name    = "ai-website-agency-preview-router"
  role             = aws_iam_role.lambda_edge_execution.arn
  handler          = "ai-website-agency-preview-router.handler"
  source_code_hash = data.archive_file.preview_router.output_base64sha256
  runtime          = "nodejs20.x"
  timeout          = 5
  memory_size      = 128
  publish          = true # required for Lambda@Edge

  tags = {
    Name = "ai-website-agency-preview-router"
  }
}

resource "aws_cloudfront_distribution" "frontend_preview" {
  provider = aws.us_east_1

  enabled             = true
  is_ipv6_enabled     = true
  comment             = "ai-website-agency preview frontend (preview.${var.base_domain})"
  default_root_object = "index.html"
  aliases             = ["preview.${var.base_domain}"]
  price_class         = "PriceClass_100"
  web_acl_id          = aws_wafv2_web_acl.cloudfront.arn
  http_version        = "http2and3"

  origin {
    domain_name = aws_s3_bucket.frontend_preview_shared.bucket_regional_domain_name
    origin_id   = "s3-frontend-preview-shared"
    s3_origin_config {
      origin_access_identity = aws_cloudfront_origin_access_identity.frontend_preview_shared.cloudfront_access_identity_path
    }
  }

  default_cache_behavior {
    target_origin_id           = "s3-frontend-preview-shared"
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["GET", "HEAD", "OPTIONS"]
    cached_methods             = ["GET", "HEAD"]
    compress                   = true
    cache_policy_id            = data.aws_cloudfront_cache_policy.caching_disabled.id # Lambda@Edge re-routes; never cache routing
    response_headers_policy_id = aws_cloudfront_response_headers_policy.security_headers.id

    lambda_function_association {
      event_type   = "origin-request"
      lambda_arn   = aws_lambda_function.preview_router.qualified_arn
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
    Name      = "ai-website-agency-frontend-preview"
    Component = "frontend-preview"
  }
}

resource "aws_s3_bucket_policy" "frontend_preview_oai" {
  bucket = aws_s3_bucket.frontend_preview_shared.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid    = "AllowCloudFrontOAI"
      Effect = "Allow"
      Principal = {
        AWS = aws_cloudfront_origin_access_identity.frontend_preview_shared.iam_arn
      }
      Action   = "s3:GetObject"
      Resource = "${aws_s3_bucket.frontend_preview_shared.arn}/*"
    }]
  })
}

resource "aws_route53_record" "preview" {
  zone_id = aws_route53_zone.ai-website-agency.zone_id
  name    = "preview.${var.base_domain}"
  type    = "A"
  alias {
    name                   = aws_cloudfront_distribution.frontend_preview.domain_name
    zone_id                = aws_cloudfront_distribution.frontend_preview.hosted_zone_id
    evaluate_target_health = false
  }
}
