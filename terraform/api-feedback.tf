# ---------------------------------------------------------------------------
# api-feedback — operator-only BFF for the iter 9.2 Feedback log.
#
#   GET /feedback?vertical=<v>&subject=<s>&limit=<n>&cursor=<c>
#     → Feedback rows for vertical <v> (default "default"),
#       newest-first, optional stage filter, cursor-paged. Payload
#       bodies are not returned (log view = who/what/when only).
#
# Read-only (DDB Query on the FEEDBACK#<vertical> partition); reuses
# the shared lambda_api IAM role. Same sync-BFF shape as api-queue.
# ---------------------------------------------------------------------------

data "archive_file" "api_feedback" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-feedback/bootstrap"
  output_path = "${path.module}/.terraform/api-feedback.zip"
}

resource "aws_lambda_function" "api_feedback" {
  filename         = data.archive_file.api_feedback.output_path
  function_name    = "ai-website-agency-api-feedback${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_feedback.output_base64sha256
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
      ITEMS_TABLE = aws_dynamodb_table.items.name
      ENVIRONMENT = var.environment
      LOG_LEVEL   = local.is_production ? "INFO" : "DEBUG"
    }
  }

  depends_on = [aws_cloudwatch_log_group.api_feedback]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_feedback_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_feedback.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_feedback" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_feedback.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "feedback_get" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /feedback"
  target             = "integrations/${aws_apigatewayv2_integration.api_feedback.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}
