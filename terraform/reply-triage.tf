# ---------------------------------------------------------------------------
# reply-triage Lambda — iter 8.5.1 + 8.5.2.
#
# reply-detector (iter 8.4) is the SOLE S3 consumer of the inbound
# bucket; it republishes a `reply.received {bucket,key}` pipeline event
# for every parseable inbound reply. reply-triage consumes that via the
# project's standard pipeline-bus → SQS → Lambda pattern (identical to
# sender/ses-feedback). This avoids the two dead ends the CI deploy
# role enforces: S3 rejects two notification destinations sharing a
# prefix, and the role cannot create default-bus EventBridge rules
# (events:PutRule/PutTargets/TagResource are denied off the pipeline
# bus, and aws-setup/ is do-not-touch).
#
# reply-triage reads the reply MIME from S3 by {bucket,key}, classifies
# it with Bedrock Haiku (replyTriage.v1) and routes (suppress +
# rejected_after_review / responded / operator inbox).
#
# Reuses the shared lambda_api role: Bedrock invoke (bedrock-iam.tf),
# SES suppression (ses.tf ses_send), DDB rw + EventBridge PutEvents,
# and s3:GetObject on inbound/* (granted by reply_detector_s3_read on
# the same role) are all already covered. Only new grant is SQS
# consume on the new main queue (mirrors sender_sqs_consume). No
# on-failure config (CI role lacks lambda:PutFunctionEventInvokeConfig
# — iter 8.4); SQS maxReceiveCount=3 → dlq["reply-triage"] (sqs.tf).
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

resource "aws_sqs_queue" "reply_triage_main" {
  name                       = "ai-website-agency-reply-triage-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 120    # > Lambda timeout (60s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["reply-triage"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, { Component = "reply-triage-main" })
}

resource "aws_sqs_queue_policy" "reply_triage_main" {
  queue_url = aws_sqs_queue.reply_triage_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.reply_triage_main.arn
      Condition = {
        ArnEquals = { "aws:SourceArn" = aws_cloudwatch_event_rule.reply_received.arn }
      }
    }]
  })
}

resource "aws_cloudwatch_event_rule" "reply_received" {
  name           = "reply-received-to-triage${local.env_suffix}"
  description    = "Route reply.received events to the reply-triage Lambda"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["reply.received"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "reply_received_to_triage" {
  rule           = aws_cloudwatch_event_rule.reply_received.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "reply-triage-sqs"
  arn            = aws_sqs_queue.reply_triage_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["reply-triage"].arn
  }
}

resource "aws_lambda_event_source_mapping" "reply_triage_sqs" {
  event_source_arn                   = aws_sqs_queue.reply_triage_main.arn
  function_name                      = aws_lambda_function.reply_triage.arn
  batch_size                         = 2
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "reply_triage_sqs_consume" {
  name = "reply-triage-sqs-consume"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage",
        "sqs:GetQueueAttributes",
        "sqs:GetQueueUrl",
        "sqs:ChangeMessageVisibility",
      ]
      Resource = aws_sqs_queue.reply_triage_main.arn
    }]
  })
}
