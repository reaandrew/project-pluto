# Pitfall #5 (tripwire c065c37): Lambda auto-creates log groups on first invocation
# WITHOUT retention. Once Terraform tries to declare them with retention, it fails
# with ResourceAlreadyExistsException. Solution: declare them explicitly here BEFORE
# the Lambda function, and add `depends_on` to the Lambda.

resource "aws_cloudwatch_log_group" "api_hello" {
  name              = "/aws/lambda/ai-website-agency-api-hello${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}

resource "aws_cloudwatch_log_group" "api_settings" {
  name              = "/aws/lambda/ai-website-agency-api-settings${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}

resource "aws_cloudwatch_log_group" "api_gateway" {
  name              = "/aws/apigateway/ai-website-agency${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}
