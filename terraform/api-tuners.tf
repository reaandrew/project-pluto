# ---------------------------------------------------------------------------
# api-tuners — operator-only BFF for the iter 9.4 tuner-delta review.
#
#   GET  /tuners?status=<s>          — PENDING TunerDeltas (default).
#   POST /tuners/{id}/apply  {ref}   — mutate live profile + version
#                                       bump + Feedback row + profile.updated.
#   POST /tuners/{id}/dismiss {ref}  — dismiss + Feedback row.
#
# Reuses the shared lambda_api role (DDB rw + EventBridge PutEvents
# already granted). Same sync-BFF shape as api-replies/api-queue.
# ---------------------------------------------------------------------------

data "archive_file" "api_tuners" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-tuners/bootstrap"
  output_path = "${path.module}/.terraform/api-tuners.zip"
}

resource "aws_lambda_function" "api_tuners" {
  filename         = data.archive_file.api_tuners.output_path
  function_name    = "ai-website-agency-api-tuners${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_tuners.output_base64sha256
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

  depends_on = [aws_cloudwatch_log_group.api_tuners]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_tuners_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_tuners.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_tuners" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_tuners.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "tuners_get" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /tuners"
  target             = "integrations/${aws_apigatewayv2_integration.api_tuners.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "tuners_apply" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /tuners/{id}/apply"
  target             = "integrations/${aws_apigatewayv2_integration.api_tuners.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "tuners_dismiss" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /tuners/{id}/dismiss"
  target             = "integrations/${aws_apigatewayv2_integration.api_tuners.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}
