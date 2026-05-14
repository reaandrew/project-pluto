# ---------------------------------------------------------------------------
# qualifier Lambda — iter 3.2. SQS-driven consumer of
# `website.audit.completed` events where detail.worthRedesigning = true.
#
# Pipeline:
#   EventBridge rule (detail-type = "website.audit.completed",
#     detail.worthRedesigning = [true])
#     → SQS main queue (`qualifier-main`)
#       → Lambda (event source mapping)
#   3 retries via SQS maxReceiveCount=3 → DLQ (`qualifier-dlq`, created
#   in sqs.tf at iter 0.C.7).
#
# Per-stage wrappers enforced at handler entry:
#   - killswitch.WithKillSwitch on stages.auditEnabled (the qualifier
#     rolls up to the audit stage per killswitch.StageMap)
#   - idempotency.WithIdempotency keyed on event.eventId
#
# Reuses the shared lambda_api IAM role for DDB read/write + EventBridge
# PutEvents (already there). One new inline policy adds SQS consume on
# the qualifier main queue.
# ---------------------------------------------------------------------------

data "archive_file" "qualifier" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/qualifier/bootstrap"
  output_path = "${path.module}/.terraform/qualifier.zip"
}

resource "aws_lambda_function" "qualifier" {
  filename         = data.archive_file.qualifier.output_path
  function_name    = "ai-website-agency-qualifier${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.qualifier.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 60 # pure-Go scoring; DDB lookups dominate
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

  depends_on = [aws_cloudwatch_log_group.qualifier]

  tags = local.common_tags
}

resource "aws_sqs_queue" "qualifier_main" {
  name                       = "ai-website-agency-qualifier-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 120    # > Lambda timeout (60s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["qualifier"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "qualifier-main"
  })
}

resource "aws_sqs_queue_policy" "qualifier_main" {
  queue_url = aws_sqs_queue.qualifier_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.qualifier_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_cloudwatch_event_rule.audit_completed_worth_redesigning.arn
        }
      }
    }]
  })
}

# EventBridge rule: route website.audit.completed events with
# worthRedesigning=true to the qualifier queue. The event pattern
# filter means audits that decided "not worth redesigning" never reach
# the qualifier — they stay in audit + stop.
resource "aws_cloudwatch_event_rule" "audit_completed_worth_redesigning" {
  name           = "audit-completed-to-qualifier${local.env_suffix}"
  description    = "Route website.audit.completed (worthRedesigning=true) to the qualifier Lambda"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["website.audit.completed"]
    detail = {
      worthRedesigning = [true]
    }
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "audit_completed_to_qualifier" {
  rule           = aws_cloudwatch_event_rule.audit_completed_worth_redesigning.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "qualifier-sqs"
  arn            = aws_sqs_queue.qualifier_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["qualifier"].arn
  }
}

resource "aws_lambda_event_source_mapping" "qualifier_sqs" {
  event_source_arn                   = aws_sqs_queue.qualifier_main.arn
  function_name                      = aws_lambda_function.qualifier.arn
  batch_size                         = 5
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "qualifier_sqs_consume" {
  name = "qualifier-sqs-consume"
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
      Resource = aws_sqs_queue.qualifier_main.arn
    }]
  })
}
