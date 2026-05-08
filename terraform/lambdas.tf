data "archive_file" "api_hello" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-hello/bootstrap"
  output_path = "${path.module}/.terraform/api-hello.zip"
}

resource "aws_lambda_function" "api_hello" {
  filename         = data.archive_file.api_hello.output_path
  function_name    = "ai-website-agency-api-hello${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_hello.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 10
  memory_size      = 256

  tracing_config {
    mode = "Active"
  }

  environment {
    variables = {
      ITEMS_TABLE = aws_dynamodb_table.items.name
      ENVIRONMENT = var.environment
      LOG_LEVEL   = local.is_production ? "INFO" : "DEBUG"
    }
  }

  # Pitfall #5: log group MUST exist before the Lambda or Lambda auto-creates one
  # without retention.
  depends_on = [aws_cloudwatch_log_group.api_hello]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_hello_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_hello.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_hello" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_hello.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "health" {
  api_id    = aws_apigatewayv2_api.main.id
  route_key = "GET /health"
  target    = "integrations/${aws_apigatewayv2_integration.api_hello.id}"
}
