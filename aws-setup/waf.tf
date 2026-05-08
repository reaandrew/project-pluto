# Single WAF WebACL in us-east-1, attached to all three CloudFront distributions.
# CloudFront-scope WebACLs MUST live in us-east-1.

resource "aws_wafv2_web_acl" "cloudfront" {
  provider = aws.us_east_1

  name        = "website-agency-cloudfront"
  description = "WAF for website-agency CloudFront distributions"
  scope       = "CLOUDFRONT"

  default_action {
    allow {}
  }

  # 1. AWS Managed Common Rule Set (with body/UA exceptions to avoid false-positives on API traffic)
  rule {
    name     = "AWS-CommonRuleSet"
    priority = 10
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
        rule_action_override {
          name = "SizeRestrictions_BODY"
          action_to_use {
            count {}
          }
        }
        rule_action_override {
          name = "NoUserAgent_HEADER"
          action_to_use {
            count {}
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "website-agency-CommonRuleSet"
      sampled_requests_enabled   = true
    }
  }

  # 2. Known bad inputs
  rule {
    name     = "AWS-KnownBadInputs"
    priority = 20
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "website-agency-KnownBadInputs"
      sampled_requests_enabled   = true
    }
  }

  # 3. IP reputation
  rule {
    name     = "AWS-IpReputation"
    priority = 30
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesAmazonIpReputationList"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "website-agency-IpReputation"
      sampled_requests_enabled   = true
    }
  }

  # 4. Per-IP rate limit (1000 requests / 5 min). Tighten after baseline.
  rule {
    name     = "RateLimitPerIP"
    priority = 100
    action {
      block {}
    }
    statement {
      rate_based_statement {
        limit              = 1000
        aggregate_key_type = "IP"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "website-agency-RateLimitPerIP"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "website-agency-cloudfront-webacl"
    sampled_requests_enabled   = true
  }

  tags = {
    Name = "website-agency-cloudfront"
  }
}
