# ---------------------------------------------------------------------------
# sender Lambda — iter 8.2. SQS-driven consumer of `email.approved`
# events (the api-email approve handler from iter 7.3 is the producer;
# 03-events.md routes email.approved → sender). SES-sends the approved
# EmailDraft from the pinned outreach address with an RFC 8058
# one-click List-Unsubscribe header; writes an EmailEvent event=sent
# and publishes email.sent.
#
# Pipeline:
#   EventBridge rule (detail-type = "email.approved")
#     → SQS main queue (`sender-main`)
#       → Lambda (event source mapping)
#   3 retries via SQS maxReceiveCount=3 → DLQ (`sender-dlq`, created in
#   sqs.tf at iter 0.C.7).
#
# Per-stage wrappers at handler entry:
#   - killswitch.WithKillSwitch on stages.outreachEnabled
#   - idempotency.WithIdempotency keyed on event.eventId (replay)
#   - permanent send-once marker on sha256(contactId+websiteId)
#
# Reuses the shared lambda_api IAM role — DDB read/write, EventBridge
# PutEvents, and SES send + suppression (`ses:SendEmail`,
# `sesv2:GetSuppressedDestination`, granted in terraform/ses.tf's
# `ses_send` policy) all covered. One new inline policy adds SQS
# consume on the new main queue.
#
# Live delivery is gated on the manual SES sandbox-out (docs/SES.md) —
# this Lambda is correct regardless; in a sandboxed/non-prod env the
# SES call fails and the record DLQs.
# ---------------------------------------------------------------------------

data "archive_file" "sender" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/sender/bootstrap"
  output_path = "${path.module}/.terraform/sender.zip"
}

resource "aws_lambda_function" "sender" {
  filename         = data.archive_file.sender.output_path
  function_name    = "ai-website-agency-sender${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.sender.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 60 # SES SendEmail + suppression check + DDB I/O
  memory_size      = 256

  tracing_config {
    mode = "Active"
  }

  # nosemgrep: terraform.aws.security.aws-lambda-environment-unencrypted.aws-lambda-environment-unencrypted
  environment {
    variables = {
      ITEMS_TABLE           = aws_dynamodb_table.items.name
      ENVIRONMENT           = var.environment
      LOG_LEVEL             = local.is_production ? "INFO" : "DEBUG"
      EVENT_BUS_NAME        = aws_cloudwatch_event_bus.pipeline.name
      SES_FROM_ADDRESS      = "outreach@outreach.${var.base_domain}"
      SES_CONFIGURATION_SET = aws_sesv2_configuration_set.outreach.configuration_set_name
      UNSUBSCRIBE_BASE      = "https://${local.api_domain}"
    }
  }

  depends_on = [aws_cloudwatch_log_group.sender]

  tags = local.common_tags
}

resource "aws_sqs_queue" "sender_main" {
  name                       = "ai-website-agency-sender-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 120    # > Lambda timeout (60s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["sender"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "sender-main"
  })
}

resource "aws_sqs_queue_policy" "sender_main" {
  queue_url = aws_sqs_queue.sender_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.sender_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_cloudwatch_event_rule.email_approved.arn
        }
      }
    }]
  })
}

resource "aws_cloudwatch_event_rule" "email_approved" {
  name           = "email-approved-to-sender${local.env_suffix}"
  description    = "Route email.approved events to the sender Lambda"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["email.approved"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "email_approved_to_sender" {
  rule           = aws_cloudwatch_event_rule.email_approved.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "sender-sqs"
  arn            = aws_sqs_queue.sender_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["sender"].arn
  }
}

resource "aws_lambda_event_source_mapping" "sender_sqs" {
  event_source_arn                   = aws_sqs_queue.sender_main.arn
  function_name                      = aws_lambda_function.sender.arn
  batch_size                         = 2
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "sender_sqs_consume" {
  name = "sender-sqs-consume"
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
      Resource = aws_sqs_queue.sender_main.arn
    }]
  })
}
