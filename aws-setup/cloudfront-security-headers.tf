# Two response-headers policies:
#   - security_headers: HSTS, frame-options, content-type-options, referrer policy.
#     CSP intentionally left wide-open in the skeleton — tighten later.
#   - cors_passthrough: for the BFF, allow credentialed requests from the production
#     and preview frontend origins.

resource "aws_cloudfront_response_headers_policy" "security_headers" {
  provider = aws.us_east_1

  name    = "ai-website-agency-security-headers"
  comment = "HSTS + standard security headers for ai-website-agency"

  security_headers_config {
    strict_transport_security {
      access_control_max_age_sec = 63072000
      include_subdomains         = true
      preload                    = true
      override                   = true
    }
    content_type_options {
      override = true
    }
    frame_options {
      frame_option = "DENY"
      override     = true
    }
    referrer_policy {
      referrer_policy = "strict-origin-when-cross-origin"
      override        = true
    }
    xss_protection {
      mode_block = true
      protection = true
      override   = true
    }
  }
}

resource "aws_cloudfront_response_headers_policy" "cors_passthrough" {
  provider = aws.us_east_1

  name    = "ai-website-agency-cors-passthrough"
  comment = "CORS for BFF — credentialed requests from production + preview frontend"

  cors_config {
    access_control_allow_credentials = true
    access_control_allow_headers {
      items = ["Authorization", "Content-Type", "Cookie", "X-Requested-With"]
    }
    access_control_allow_methods {
      items = ["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"]
    }
    access_control_allow_origins {
      items = [
        "https://${var.base_domain}",
        "https://preview.${var.base_domain}",
      ]
    }
    # Note: Set-Cookie is a forbidden CORS expose header by spec; CloudFront enforces this.
    # Browsers handle Set-Cookie via the cookie store regardless of CORS expose headers.
    access_control_expose_headers {
      items = ["X-Request-Id"]
    }
    access_control_max_age_sec = 300
    origin_override            = true
  }
}
