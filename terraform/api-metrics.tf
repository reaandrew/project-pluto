# ---------------------------------------------------------------------------
# api-metrics Lambda — iter 1.4. Two routes today:
#
#   GET  /metrics/discoveries        → recent businesses + 7-day count
#   POST /metrics/discoveries/run    → synchronous Invoke of the discover Lambda
#
# Operator-only via the standard Cognito JWT authorizer + handler-side
# operator-group check. Reuses the shared lambda_api IAM role; one new
# policy resource attached below grants the role permission to call
# lambda:InvokeFunction on the discover Lambda specifically (the
# narrowest grant — no wildcard).
# ---------------------------------------------------------------------------

data "archive_file" "api_metrics" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-metrics/bootstrap"
  output_path = "${path.module}/.terraform/api-metrics.zip"
}

resource "aws_lambda_function" "api_metrics" {
  filename         = data.archive_file.api_metrics.output_path
  function_name    = "ai-website-agency-api-metrics${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_metrics.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 60 # Run-now is synchronous; discover Lambda can take a while
  memory_size      = 256

  tracing_config {
    mode = "Active"
  }

  # nosemgrep: terraform.aws.security.aws-lambda-environment-unencrypted.aws-lambda-environment-unencrypted
  environment {
    variables = {
      ITEMS_TABLE            = aws_dynamodb_table.items.name
      ENVIRONMENT            = var.environment
      LOG_LEVEL              = local.is_production ? "INFO" : "DEBUG"
      DISCOVER_FUNCTION_NAME = aws_lambda_function.discover.function_name
    }
  }

  depends_on = [aws_cloudwatch_log_group.api_metrics]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_metrics_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_metrics.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_metrics" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_metrics.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "metrics_discoveries_get" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /metrics/discoveries"
  target             = "integrations/${aws_apigatewayv2_integration.api_metrics.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "metrics_discoveries_run" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /metrics/discoveries/run"
  target             = "integrations/${aws_apigatewayv2_integration.api_metrics.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

# Allow api-metrics to invoke the discover Lambda directly. Narrowest
# possible scope — just one function ARN. When a future Lambda needs
# similar "trigger this discover from a BFF endpoint" wiring, add it
# here alongside, never widen to a wildcard.
resource "aws_iam_role_policy" "invoke_discover_for_metrics" {
  name = "invoke-discover-for-metrics"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["lambda:InvokeFunction"]
      Resource = aws_lambda_function.discover.arn
    }]
  })
}
