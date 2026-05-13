# Pitfall #5 (tripwire c065c37): Lambda auto-creates log groups on first invocation
# WITHOUT retention. Once Terraform tries to declare them with retention, it fails
# with ResourceAlreadyExistsException. Solution: declare them explicitly here BEFORE
# the Lambda function, and add `depends_on` to the Lambda.
#
# nosemgrep rationale (aws-cloudwatch-log-group-unencrypted): CloudWatch
# Logs are encrypted at rest by AWS by default with the AWS-managed key.
# Adding a customer-managed CMK (`kms_key_id`) is the rule's preference
# but adds operational cost (key rotation, IAM, per-env CMKs) without
# materially raising the bar against this project's threat model — the
# logs we emit are structured slogger lines that the spec already
# forbids from containing secrets (no passcode cleartext, no JWTs, no
# PII beyond what's in the audited business-domain field). The single
# CMK we DO maintain is `aws_kms_key.passcode_cleartext` (terraform/kms.tf)
# scoped to passcode envelope encryption — extending it to cover all
# Lambda log groups would weaken that scoping.

# nosemgrep: terraform.aws.security.aws-cloudwatch-log-group-unencrypted.aws-cloudwatch-log-group-unencrypted
resource "aws_cloudwatch_log_group" "api_hello" {
  name              = "/aws/lambda/ai-website-agency-api-hello${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}

# nosemgrep: terraform.aws.security.aws-cloudwatch-log-group-unencrypted.aws-cloudwatch-log-group-unencrypted
resource "aws_cloudwatch_log_group" "api_settings" {
  name              = "/aws/lambda/ai-website-agency-api-settings${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}

# nosemgrep: terraform.aws.security.aws-cloudwatch-log-group-unencrypted.aws-cloudwatch-log-group-unencrypted
resource "aws_cloudwatch_log_group" "api_gateway" {
  name              = "/aws/apigateway/ai-website-agency${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}

# nosemgrep: terraform.aws.security.aws-cloudwatch-log-group-unencrypted.aws-cloudwatch-log-group-unencrypted
resource "aws_cloudwatch_log_group" "cost_rollover" {
  name              = "/aws/lambda/ai-website-agency-cost-rollover${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}

# nosemgrep: terraform.aws.security.aws-cloudwatch-log-group-unencrypted.aws-cloudwatch-log-group-unencrypted
resource "aws_cloudwatch_log_group" "api_targeting" {
  name              = "/aws/lambda/ai-website-agency-api-targeting${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}

# nosemgrep: terraform.aws.security.aws-cloudwatch-log-group-unencrypted.aws-cloudwatch-log-group-unencrypted
resource "aws_cloudwatch_log_group" "discover" {
  name              = "/aws/lambda/ai-website-agency-discover${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}

# nosemgrep: terraform.aws.security.aws-cloudwatch-log-group-unencrypted.aws-cloudwatch-log-group-unencrypted
resource "aws_cloudwatch_log_group" "api_metrics" {
  name              = "/aws/lambda/ai-website-agency-api-metrics${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}
