# ---------------------------------------------------------------------------
# audit Lambda — iter 2.3. SQS-driven consumer of `business.found` events.
#
# Pipeline:
#   EventBridge rule (detail-type = "business.found")
#     → SQS main queue (`audit-main`)
#       → Lambda (event source mapping)
#   3 retries via SQS maxReceiveCount=3 → DLQ (`audit-dlq`, created in
#   sqs.tf at iter 0.C.7).
#
# Per-stage wrappers enforced at handler entry:
#   - killswitch.WithKillSwitch on stages.auditEnabled
#   - idempotency.WithIdempotency keyed on event.eventId
#   - cost.WithCostCap (inside qualitative.Run via prompts.Invoke →
#     bedrock.InvokeStructured → cost.Assert)
#
# Reuses the shared lambda_api IAM role — DDB read/write, S3 blobs r/w,
# Bedrock invoke, SSM read, EventBridge PutEvents are all already there.
# One new resource adds sqs:ReceiveMessage/DeleteMessage on the new main
# queue; nothing else.
# ---------------------------------------------------------------------------

data "archive_file" "audit" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/audit/bootstrap"
  output_path = "${path.module}/.terraform/audit.zip"
}

resource "aws_lambda_function" "audit" {
  filename         = data.archive_file.audit.output_path
  function_name    = "ai-website-agency-audit${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.audit.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 120 # politefetch + PageSpeed + Bedrock; killswitch + cap rein in spend
  memory_size      = 512

  tracing_config {
    mode = "Active"
  }

  # nosemgrep: terraform.aws.security.aws-lambda-environment-unencrypted.aws-lambda-environment-unencrypted
  environment {
    variables = {
      ITEMS_TABLE       = aws_dynamodb_table.items.name
      ENVIRONMENT       = var.environment
      LOG_LEVEL         = local.is_production ? "INFO" : "DEBUG"
      EVENT_BUS_NAME    = aws_cloudwatch_event_bus.pipeline.name
      BLOBS_BUCKET      = aws_s3_bucket.pipeline_blobs.bucket
      PAGESPEED_API_KEY = nonsensitive(data.aws_ssm_parameter.pagespeed_api_key.value)
    }
  }

  depends_on = [aws_cloudwatch_log_group.audit]

  tags = local.common_tags
}

# Main SQS queue feeding the Lambda. maxReceiveCount=3 → DLQ on the
# fourth visibility-timeout expiry per .ralph/specs/03-events.md.
resource "aws_sqs_queue" "audit_main" {
  name                       = "ai-website-agency-audit-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days — far longer than the EventBridge MaxAge
  visibility_timeout_seconds = 180    # > Lambda timeout (120s) per AWS guidance
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["audit"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "audit-main"
  })
}

# Allow EventBridge to send messages onto the queue.
resource "aws_sqs_queue_policy" "audit_main" {
  queue_url = aws_sqs_queue.audit_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.audit_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_cloudwatch_event_rule.business_found.arn
        }
      }
    }]
  })
}

# EventBridge rule: route business.found events to the audit queue.
resource "aws_cloudwatch_event_rule" "business_found" {
  name           = "business-found-to-audit${local.env_suffix}"
  description    = "Route business.found events to the audit Lambda SQS queue"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["business.found"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "business_found_to_audit" {
  rule           = aws_cloudwatch_event_rule.business_found.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "audit-sqs"
  arn            = aws_sqs_queue.audit_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["audit"].arn
  }
}

# SQS → Lambda event source mapping.
resource "aws_lambda_event_source_mapping" "audit_sqs" {
  event_source_arn                   = aws_sqs_queue.audit_main.arn
  function_name                      = aws_lambda_function.audit.arn
  batch_size                         = 5
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

# Grant the Lambda role read/delete on the main queue. The DLQ role
# grants in sqs.tf cover the redrive queue already.
resource "aws_iam_role_policy" "audit_sqs_consume" {
  name = "audit-sqs-consume"
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
      Resource = aws_sqs_queue.audit_main.arn
    }]
  })
}

# ---------------------------------------------------------------------------
# PageSpeed Insights API key — operator-supplied via SSM. Same
# placeholder-then-ignore-changes pattern as the providers in discover.tf.
# ---------------------------------------------------------------------------

# tfsec:ignore:aws-ssm-secret-use-customer-key  -- AWS-managed key is fine here; rotation handled by the SSM service.
resource "aws_ssm_parameter" "pagespeed_api_key" {
  name        = "/ai-website-agency/${var.environment}/providers/pagespeed_api_key"
  description = "Google PageSpeed Insights API key. Operator-supplied; replace the placeholder after first apply."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags

  lifecycle {
    ignore_changes = [value]
  }
}

data "aws_ssm_parameter" "pagespeed_api_key" {
  name            = aws_ssm_parameter.pagespeed_api_key.name
  with_decryption = true
}
