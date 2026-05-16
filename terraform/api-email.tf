# ---------------------------------------------------------------------------
# api-email — operator-only BFF routes for the iter 7.3 email-review
# page at /queue/[id]/email.
#
#   GET   /candidates/{businessId}/email                   → business + latest EmailDraft (real body — operator-authorised)
#   PATCH /candidates/{businessId}/email/{emailId}          → edit subject/body; feedback.captured(edit), payloads redacted
#   POST  /candidates/{businessId}/email/{emailId}/approve  → status→approved, publish email.approved + feedback (redacted)
#   POST  /candidates/{businessId}/email/{emailId}/reject   → status→rejected, publish email.rejected + feedback (redacted)
#
# Reuses the shared lambda_api IAM role — DDB read/write, EventBridge
# PutEvents, and kms:Decrypt (granted in kms.tf since iter 5.6b) all
# covered. kms:Decrypt is used ONLY to recover the cleartext passcode
# so the feedback OriginalPayload/EditedPayload can be redacted to the
# {{PASSCODE}} placeholder (10-quality-rules § Rule 2).
# ---------------------------------------------------------------------------

data "archive_file" "api_email" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/api-email/bootstrap"
  output_path = "${path.module}/.terraform/api-email.zip"
}

resource "aws_lambda_function" "api_email" {
  filename         = data.archive_file.api_email.output_path
  function_name    = "ai-website-agency-api-email${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.api_email.output_base64sha256
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
      # kms:Decrypt the Website.passcodeCipher to redact the cleartext
      # out of feedback payloads. Same CMK + already-granted Decrypt as
      # api-website/email-draft (key policy in terraform/kms.tf).
      PASSCODE_KMS_KEY_ID = aws_kms_alias.passcode_cleartext.target_key_arn
    }
  }

  depends_on = [aws_cloudwatch_log_group.api_email]

  tags = local.common_tags
}

resource "aws_lambda_permission" "api_email_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api_email.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.main.execution_arn}/*/*"
}

resource "aws_apigatewayv2_integration" "api_email" {
  api_id                 = aws_apigatewayv2_api.main.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api_email.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "candidate_email_get" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "GET /candidates/{businessId}/email"
  target             = "integrations/${aws_apigatewayv2_integration.api_email.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "candidate_email_patch" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "PATCH /candidates/{businessId}/email/{emailId}"
  target             = "integrations/${aws_apigatewayv2_integration.api_email.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "candidate_email_approve" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /candidates/{businessId}/email/{emailId}/approve"
  target             = "integrations/${aws_apigatewayv2_integration.api_email.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}

resource "aws_apigatewayv2_route" "candidate_email_reject" {
  api_id             = aws_apigatewayv2_api.main.id
  route_key          = "POST /candidates/{businessId}/email/{emailId}/reject"
  target             = "integrations/${aws_apigatewayv2_integration.api_email.id}"
  authorization_type = "JWT"
  authorizer_id      = aws_apigatewayv2_authorizer.cognito.id
}
