# ---------------------------------------------------------------------------
# backlog-promoter Lambda — iter 3.3. SQS-driven consumer of
# `queue.slot.freed` events. EventBridge rule routes the event to the
# main queue; the Lambda picks the highest-priority awaitingPromotion=true
# Business, clears the flag, and emits website.qualified so preview gen
# wakes up.
#
# Note: nobody emits queue.slot.freed yet (the producer lands alongside
# the operator approve/reject UI in iter 6.x). This Lambda + rule sit
# quiescent until then — wiring them now keeps the topology in code.
#
# Per-stage wrappers enforced at handler entry:
#   - killswitch.WithKillSwitch on StageAudit (the promoter rolls up to
#     the audit stage — same as the qualifier)
#   - idempotency.WithIdempotency keyed on event.eventId
#
# Reuses the shared lambda_api IAM role. One new inline policy grants
# SQS consume on the new main queue.
# ---------------------------------------------------------------------------

data "archive_file" "backlog_promoter" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/backlog-promoter/bootstrap"
  output_path = "${path.module}/.terraform/backlog-promoter.zip"
}

resource "aws_lambda_function" "backlog_promoter" {
  filename         = data.archive_file.backlog_promoter.output_path
  function_name    = "ai-website-agency-backlog-promoter${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.backlog_promoter.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 30 # pure DDB I/O + one PutEvents
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
    }
  }

  depends_on = [aws_cloudwatch_log_group.backlog_promoter]

  tags = local.common_tags
}

resource "aws_sqs_queue" "backlog_promoter_main" {
  name                       = "ai-website-agency-backlog-promoter-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 60     # > Lambda timeout (30s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["backlog-promoter"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "backlog-promoter-main"
  })
}

resource "aws_sqs_queue_policy" "backlog_promoter_main" {
  queue_url = aws_sqs_queue.backlog_promoter_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.backlog_promoter_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_cloudwatch_event_rule.queue_slot_freed.arn
        }
      }
    }]
  })
}

resource "aws_cloudwatch_event_rule" "queue_slot_freed" {
  name           = "queue-slot-freed-to-promoter${local.env_suffix}"
  description    = "Route queue.slot.freed events to the backlog-promoter Lambda"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["queue.slot.freed"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "queue_slot_freed_to_promoter" {
  rule           = aws_cloudwatch_event_rule.queue_slot_freed.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "backlog-promoter-sqs"
  arn            = aws_sqs_queue.backlog_promoter_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["backlog-promoter"].arn
  }
}

resource "aws_lambda_event_source_mapping" "backlog_promoter_sqs" {
  event_source_arn                   = aws_sqs_queue.backlog_promoter_main.arn
  function_name                      = aws_lambda_function.backlog_promoter.arn
  batch_size                         = 5
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "backlog_promoter_sqs_consume" {
  name = "backlog-promoter-sqs-consume"
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
      Resource = aws_sqs_queue.backlog_promoter_main.arn
    }]
  })
}
