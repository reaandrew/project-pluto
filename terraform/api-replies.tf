# ---------------------------------------------------------------------------
# api-replies — operator-only BFF for the iter 8.5.3 reply-triage inbox.
#
#   GET  /replies?status=<s>&category=<c>&limit=<n>&cursor=<c>
#     → ReplyTriage items in triage-state <s> (default operator_inbox),
#       newest-first via gsi1, optional category filter, cursor-paged.
#   POST /replies/{id}/reclassify   body {ref,newCategory,notes}
#     → operator override; item → reviewed; attributed replies get the
#       same Business.status side-effect the classifier applies.
#
# Reuses the shared lambda_api IAM role — DDB read/write is already
# granted there. No EventBridge / KMS / SSM needs.
# ---------------------------------------------------------------------------

data "archive_file" "api_replies" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-replies/bootstrap"
  output_path = "${path.module}/.terraform/api-replies.zip"
}

resource "aws_lambda_function" "api_replies" {
  filename         = data.archive_file.api_replies.output_path
  function_name    = "ai-website-agency-api-replies${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_replies.output_base64sha256
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

  depends_on = [aws_cloudwatch_log_group.api_replies]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_replies_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_replies.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_replies" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_replies.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "replies_get" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /replies"
  target             = "integrations/${aws_apigatewayv2_integration.api_replies.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "replies_reclassify" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /replies/{id}/reclassify"
  target             = "integrations/${aws_apigatewayv2_integration.api_replies.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}
