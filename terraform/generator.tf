# ---------------------------------------------------------------------------
# generator Lambda — iter 5.2. SQS-driven consumer of `spec.approved`
# events. Renders the approved Spec to a static HTML page via
# lambdas/pkg/sitebundle + lambdas/pkg/components, uploads it to S3 at
# generated/<websiteId>/index.html, persists the Website row, and
# publishes website.generated. The iter 5.3 publisher then copies S3
# → R2 + issues the passcode.
#
# Per-stage wrappers enforced at handler entry:
#   - killswitch.WithKillSwitch on stages.previewEnabled
#   - idempotency.WithIdempotency keyed on event.eventId
#
# Reuses the shared lambda_api IAM role — DDB read/write, S3 blobs
# r/w, EventBridge PutEvents already granted there. One new inline
# policy adds SQS consume on the new main queue.
# ---------------------------------------------------------------------------

data "archive_file" "generator" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/generator/bootstrap"
  output_path = "${path.module}/.terraform/generator.zip"
}

resource "aws_lambda_function" "generator" {
  filename         = data.archive_file.generator.output_path
  function_name    = "ai-website-agency-generator${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.generator.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 60 # pure-Go rendering + one S3 PutObject; no Bedrock
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
      BLOBS_BUCKET   = aws_s3_bucket.pipeline_blobs.bucket
    }
  }

  depends_on = [aws_cloudwatch_log_group.generator]

  tags = local.common_tags
}

resource "aws_sqs_queue" "generator_main" {
  name                       = "ai-website-agency-generator-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 120    # > Lambda timeout (60s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["generator"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "generator-main"
  })
}

resource "aws_sqs_queue_policy" "generator_main" {
  queue_url = aws_sqs_queue.generator_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.generator_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_cloudwatch_event_rule.spec_approved.arn
        }
      }
    }]
  })
}

resource "aws_cloudwatch_event_rule" "spec_approved" {
  name           = "spec-approved-to-generator${local.env_suffix}"
  description    = "Route spec.approved events to the generator Lambda"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["spec.approved"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "spec_approved_to_generator" {
  rule           = aws_cloudwatch_event_rule.spec_approved.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "generator-sqs"
  arn            = aws_sqs_queue.generator_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["generator"].arn
  }
}

resource "aws_lambda_event_source_mapping" "generator_sqs" {
  event_source_arn                   = aws_sqs_queue.generator_main.arn
  function_name                      = aws_lambda_function.generator.arn
  batch_size                         = 3
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "generator_sqs_consume" {
  name = "generator-sqs-consume"
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
      Resource = aws_sqs_queue.generator_main.arn
    }]
  })
}
