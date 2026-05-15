# ---------------------------------------------------------------------------
# api-website — operator-only BFF routes for the site-preview half of the
# iter 5.6 /queue/[id] page.
#
#   GET   /candidates/{businessId}/website                    → business + latest Website (sanitised; no passcode material)
#   POST  /candidates/{businessId}/website/{websiteId}/approve → status→approved, publish website.approved + feedback.captured
#   POST  /candidates/{businessId}/website/{websiteId}/reject  → status→rejected, publish website.rejected_after_review + feedback.captured
#
# Reuses the shared lambda_api IAM role — DDB read/write + EventBridge
# PutEvents already granted. Regenerate-site / Regenerate-passcode land
# on this same Lambda in iter 5.6b.
# ---------------------------------------------------------------------------

data "archive_file" "api_website" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-website/bootstrap"
  output_path = "${path.module}/.terraform/api-website.zip"
}

resource "aws_lambda_function" "api_website" {
  filename         = data.archive_file.api_website.output_path
  function_name    = "ai-website-agency-api-website${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_website.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 15
  memory_size      = 256

  tracing_config {
    mode = "Active"
  }

  # nosemgrep: terraform.aws.security.aws-lambda-environment-unencrypted.aws-lambda-environment-unencrypted
  environment {
    variables = {
      ITEMS_TABLE    = aws_dynamodb_table.items.name
      ENVIRONMENT    = var.environment
      LOG_LEVEL      = local.is_production ? "INFO" : "DEBUG"
      EVENT_BUS_NAME = aws_cloudwatch_event_bus.pipeline.name
    }
  }

  depends_on = [aws_cloudwatch_log_group.api_website]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_website_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_website.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_website" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_website.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "candidate_website_get" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /candidates/{businessId}/website"
  target             = "integrations/${aws_apigatewayv2_integration.api_website.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "candidate_website_approve" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /candidates/{businessId}/website/{websiteId}/approve"
  target             = "integrations/${aws_apigatewayv2_integration.api_website.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "candidate_website_reject" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /candidates/{businessId}/website/{websiteId}/reject"
  target             = "integrations/${aws_apigatewayv2_integration.api_website.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}
