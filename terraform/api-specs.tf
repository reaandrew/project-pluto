# ---------------------------------------------------------------------------
# api-specs — operator-only BFF routes for the iter 4.3 /queue/[id] page.
#
#   GET    /candidates/{businessId}                       → business + latest spec + audit summary
#   GET    /candidates/{businessId}/specs                 → list specs for a business
#   PATCH  /candidates/{businessId}/specs/{specId}        → edit content (version bump, status stays draft)
#   POST   /candidates/{businessId}/specs/{specId}/approve → flip to approved, publish spec.approved + feedback.captured
#   POST   /candidates/{businessId}/specs/{specId}/reject  → flip to rejected, publish spec.rejected + feedback.captured
#
# Reuses the shared lambda_api IAM role — DDB read/write + EventBridge
# PutEvents already granted.
# ---------------------------------------------------------------------------

data "archive_file" "api_specs" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-specs/bootstrap"
  output_path = "${path.module}/.terraform/api-specs.zip"
}

resource "aws_lambda_function" "api_specs" {
  filename         = data.archive_file.api_specs.output_path
  function_name    = "ai-website-agency-api-specs${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_specs.output_base64sha256
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

  depends_on = [aws_cloudwatch_log_group.api_specs]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_specs_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_specs.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_specs" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_specs.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "candidate_get" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /candidates/{businessId}"
  target             = "integrations/${aws_apigatewayv2_integration.api_specs.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "candidate_specs_list" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /candidates/{businessId}/specs"
  target             = "integrations/${aws_apigatewayv2_integration.api_specs.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "candidate_spec_patch" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "PATCH /candidates/{businessId}/specs/{specId}"
  target             = "integrations/${aws_apigatewayv2_integration.api_specs.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "candidate_spec_approve" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /candidates/{businessId}/specs/{specId}/approve"
  target             = "integrations/${aws_apigatewayv2_integration.api_specs.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "candidate_spec_reject" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /candidates/{businessId}/specs/{specId}/reject"
  target             = "integrations/${aws_apigatewayv2_integration.api_specs.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}
