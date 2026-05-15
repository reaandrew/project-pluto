# ---------------------------------------------------------------------------
# api-queue — operator-only BFF for the iter 6.1 review-queue list.
#
#   GET /queue?status=<s>&limit=<n>&cursor=<c>
#     → Businesses in review status <s> (default awaiting_review),
#       ordered by priorityScore DESC via the gsi1 index, cursor-paginated.
#
# Read-only (DDB gsi1 Query); reuses the shared lambda_api IAM role —
# DDB read is already granted there. No EventBridge / KMS / SSM needs.
# ---------------------------------------------------------------------------

data "archive_file" "api_queue" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-queue/bootstrap"
  output_path = "${path.module}/.terraform/api-queue.zip"
}

resource "aws_lambda_function" "api_queue" {
  filename         = data.archive_file.api_queue.output_path
  function_name    = "ai-website-agency-api-queue${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_queue.output_base64sha256
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

  depends_on = [aws_cloudwatch_log_group.api_queue]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_queue_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_queue.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_queue" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_queue.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "queue_get" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /queue"
  target             = "integrations/${aws_apigatewayv2_integration.api_queue.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}
