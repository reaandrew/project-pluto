# ---------------------------------------------------------------------------
# reply-triage Lambda — iter 8.5.1 + 8.5.2.
#
# SECOND consumer of the SES inbound bucket (the first is reply-detector,
# iter 8.4). Both get the same s3:ObjectCreated event: reply-detector is
# fast and flips Business.status=responded; reply-triage reads the reply
# text, classifies it with Bedrock Haiku (replyTriage.v1) and routes
# (suppress + rejected_after_review / responded / operator inbox).
#
# Reuses the shared lambda_api role: Bedrock invoke (bedrock-iam.tf),
# SES suppression (ses.tf ses_send), DDB rw + EventBridge PutEvents are
# all already granted. Only new grant is scoped s3:GetObject on the
# inbound bucket. No on-failure DLQ / event-invoke-config (the CI
# deploy role lacks lambda:PutFunctionEventInvokeConfig — see iter 8.4;
# EventBridge retries + the 90d bucket lifecycle keep replies safe).
#
# Trigger: the inbound bucket emits to EventBridge; a single rule in
# reply-detector.tf fans "Object Created" to BOTH reply-detector and
# reply-triage (S3 forbids two notification destinations with the same
# prefix+event, so direct S3→Lambda fan-out is not possible).
# ---------------------------------------------------------------------------

data "archive_file" "reply_triage" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/reply-triage/bootstrap"
  output_path = "${path.module}/.terraform/reply-triage.zip"
}

resource "aws_lambda_function" "reply_triage" {
  filename         = data.archive_file.reply_triage.output_path
  function_name    = "ai-website-agency-reply-triage${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.reply_triage.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 60 # Bedrock classify + DDB + SES suppression
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
      REPLY_DOMAIN   = "outreach.${var.base_domain}"
    }
  }

  depends_on = [aws_cloudwatch_log_group.reply_triage]

  tags = local.common_tags
}

# Invocation permission (EventBridge principal) + the S3→EventBridge
# rule fan-out live in reply-detector.tf alongside the shared inbound
# bucket + rule.

resource "aws_iam_role_policy" "reply_triage_s3_read" {
  name = "reply-triage-s3-read"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "s3:GetObject"
      Resource = "${aws_s3_bucket.inbound_mail.arn}/inbound/*"
    }]
  })
}
