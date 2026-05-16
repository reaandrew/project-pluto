# ---------------------------------------------------------------------------
# email-draft Lambda — iter 7.2b. SQS-driven consumer of
# `website.approved` events (the api-website approve handler from iter
# 5.6a is the producer; 03-events.md routes website.approved →
# email-draft — see the package doc for the trigger-name
# reconciliation). Runs the Haiku 4.5 email.v1 prompt to produce an
# EmailDraft row in `draft` status; operators approve via the iter 7.3
# UI.
#
# Pipeline:
#   EventBridge rule (detail-type = "website.approved")
#     → SQS main queue (`email-draft-main`)
#       → Lambda (event source mapping)
#   3 retries via SQS maxReceiveCount=3 → DLQ (`email-draft-dlq`,
#   created in sqs.tf at iter 0.C.7).
#
# Per-stage wrappers at handler entry:
#   - killswitch.WithKillSwitch on stages.outreachEnabled
#   - idempotency.WithIdempotency keyed on event.eventId
#   - cost.WithCostCap (inside emaildraft.Run → bedrock.InvokeStructured
#     → cost.Assert against Budgets.DailyEmailUsd, stage "email")
#
# Reuses the shared lambda_api IAM role — DDB read/write, Bedrock
# invoke (Haiku 4.5 already in bedrock-iam.tf), EventBridge PutEvents,
# and kms:Decrypt (granted in kms.tf since iter 5.6b) all covered. One
# new inline policy adds SQS consume on the new main queue.
# ---------------------------------------------------------------------------

data "archive_file" "email_draft" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/email-draft/bootstrap"
  output_path = "${path.module}/.terraform/email-draft.zip"
}

resource "aws_lambda_function" "email_draft" {
  filename         = data.archive_file.email_draft.output_path
  function_name    = "ai-website-agency-email-draft${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.email_draft.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 120 # Haiku 4.5 + KMS decrypt + DDB I/O
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
      # KMS-decrypts Website.passcodeCipher. Same CMK + already-granted
      # kms:Decrypt as api-website (key policy in terraform/kms.tf).
      PASSCODE_KMS_KEY_ID = aws_kms_alias.passcode_cleartext.target_key_arn
    }
  }

  depends_on = [aws_cloudwatch_log_group.email_draft]

  tags = local.common_tags
}

resource "aws_sqs_queue" "email_draft_main" {
  name                       = "ai-website-agency-email-draft-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 240    # > Lambda timeout (120s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["email-draft"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "email-draft-main"
  })
}

resource "aws_sqs_queue_policy" "email_draft_main" {
  queue_url = aws_sqs_queue.email_draft_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.email_draft_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_cloudwatch_event_rule.website_approved.arn
        }
      }
    }]
  })
}

resource "aws_cloudwatch_event_rule" "website_approved" {
  name           = "website-approved-to-email-draft${local.env_suffix}"
  description    = "Route website.approved events to the email-draft Lambda"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["website.approved"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "website_approved_to_email_draft" {
  rule           = aws_cloudwatch_event_rule.website_approved.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "email-draft-sqs"
  arn            = aws_sqs_queue.email_draft_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["email-draft"].arn
  }
}

resource "aws_lambda_event_source_mapping" "email_draft_sqs" {
  event_source_arn                   = aws_sqs_queue.email_draft_main.arn
  function_name                      = aws_lambda_function.email_draft.arn
  batch_size                         = 2
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "email_draft_sqs_consume" {
  name = "email-draft-sqs-consume"
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
      Resource = aws_sqs_queue.email_draft_main.arn
    }]
  })
}
