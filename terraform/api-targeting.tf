# ---------------------------------------------------------------------------
# api-targeting — CRUD on TargetingProfile rows. Operator-only.
#
# Five routes, all JWT-authorized at the gateway AND operator-group-
# checked in the handler (same posture as api-settings, iter 0.F.2):
#
#   GET    /targeting        → list
#   GET    /targeting/{id}   → fetch one
#   POST   /targeting        → create
#   PATCH  /targeting/{id}   → update (If-Match etag required)
#   DELETE /targeting/{id}   → delete
#
# Reuses the shared lambda_api IAM role — DDB read/write on the items
# table is already granted there.
# ---------------------------------------------------------------------------

data "archive_file" "api_targeting" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-targeting/bootstrap"
  output_path = "${path.module}/.terraform/api-targeting.zip"
}

resource "aws_lambda_function" "api_targeting" {
  filename         = data.archive_file.api_targeting.output_path
  function_name    = "ai-website-agency-api-targeting${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_targeting.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 10
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

  depends_on = [aws_cloudwatch_log_group.api_targeting]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_targeting_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_targeting.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_targeting" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_targeting.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "targeting_list" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /targeting"
  target             = "integrations/${aws_apigatewayv2_integration.api_targeting.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "targeting_get" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /targeting/{id}"
  target             = "integrations/${aws_apigatewayv2_integration.api_targeting.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "targeting_create" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /targeting"
  target             = "integrations/${aws_apigatewayv2_integration.api_targeting.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "targeting_update" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "PATCH /targeting/{id}"
  target             = "integrations/${aws_apigatewayv2_integration.api_targeting.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "targeting_delete" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "DELETE /targeting/{id}"
  target             = "integrations/${aws_apigatewayv2_integration.api_targeting.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}
