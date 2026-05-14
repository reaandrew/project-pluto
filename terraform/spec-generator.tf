# ---------------------------------------------------------------------------
# spec-generator Lambda — iter 4.2b. SQS-driven consumer of
# `website.qualified` events. Runs the Sonnet 4.6 spec.v1 prompt to
# produce a Spec row in `draft` status; operators approve via the iter
# 4.3 UI.
#
# Pipeline:
#   EventBridge rule (detail-type = "website.qualified")
#     → SQS main queue (`spec-generator-main`)
#       → Lambda (event source mapping)
#   3 retries via SQS maxReceiveCount=3 → DLQ (`spec-generator-dlq`,
#   created in sqs.tf at iter 0.C.7).
#
# Per-stage wrappers enforced at handler entry:
#   - killswitch.WithKillSwitch on stages.previewEnabled
#     (spec-generator rolls up to the preview stage per killswitch.StageMap)
#   - idempotency.WithIdempotency keyed on event.eventId
#   - cost.WithCostCap (inside spec.Run → bedrock.InvokeStructured →
#     cost.Assert against Budgets.DailyBedrockUsd)
#
# Reuses the shared lambda_api IAM role — DDB read/write, Bedrock
# invoke (Sonnet 4.6 already in bedrock-iam.tf), EventBridge PutEvents
# all granted. One new inline policy adds SQS consume on the new main
# queue.
# ---------------------------------------------------------------------------

data "archive_file" "spec_generator" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/spec-generator/bootstrap"
  output_path = "${path.module}/.terraform/spec-generator.zip"
}

resource "aws_lambda_function" "spec_generator" {
  filename         = data.archive_file.spec_generator.output_path
  function_name    = "ai-website-agency-spec-generator${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.spec_generator.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 180 # Sonnet 4.6 + DDB I/O; killswitch + cap rein in spend
  memory_size      = 512

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

  depends_on = [aws_cloudwatch_log_group.spec_generator]

  tags = local.common_tags
}

resource "aws_sqs_queue" "spec_generator_main" {
  name                       = "ai-website-agency-spec-generator-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 240    # > Lambda timeout (180s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["spec-generator"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "spec-generator-main"
  })
}

resource "aws_sqs_queue_policy" "spec_generator_main" {
  queue_url = aws_sqs_queue.spec_generator_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.spec_generator_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_cloudwatch_event_rule.website_qualified.arn
        }
      }
    }]
  })
}

# EventBridge rule: route website.qualified events to the
# spec-generator queue.
resource "aws_cloudwatch_event_rule" "website_qualified" {
  name           = "website-qualified-to-spec-generator${local.env_suffix}"
  description    = "Route website.qualified events to the spec-generator Lambda"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["website.qualified"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "website_qualified_to_spec" {
  rule           = aws_cloudwatch_event_rule.website_qualified.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "spec-generator-sqs"
  arn            = aws_sqs_queue.spec_generator_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["spec-generator"].arn
  }
}

resource "aws_lambda_event_source_mapping" "spec_generator_sqs" {
  event_source_arn                   = aws_sqs_queue.spec_generator_main.arn
  function_name                      = aws_lambda_function.spec_generator.arn
  batch_size                         = 2
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "spec_generator_sqs_consume" {
  name = "spec-generator-sqs-consume"
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
      Resource = aws_sqs_queue.spec_generator_main.arn
    }]
  })
}
