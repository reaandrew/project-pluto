# ---------------------------------------------------------------------------
# ses-feedback Lambda — iter 8.3. SNS-driven consumer of SES bounce /
# complaint / delivery notifications (the configuration-set event
# destination `feedback_sns` in terraform/ses.tf is the producer).
#
# Pipeline:
#   SES configuration set event destination (BOUNCE/COMPLAINT/DELIVERY/…)
#     → SNS topic `ses_feedback` (terraform/ses.tf)
#       → SQS main queue (`ses-feedback-main`)
#         → Lambda (event source mapping)
#   3 retries via SQS maxReceiveCount=3 → DLQ (`ses-feedback-dlq`,
#   created in sqs.tf at iter 0.C).
#
# raw_message_delivery is intentionally LEFT OFF — the Lambda parses the
# SNS envelope (Type/Message) and then the SES JSON inside Message.
#
# Per-stage wrappers at handler entry:
#   - killswitch.WithKillSwitch on stages.outreachEnabled
#   - idempotency.WithIdempotency keyed on sesMessageId + ":" + kind
#
# IAM: reuses the shared lambda_api role. Suppression-list writes
# (`sesv2:PutSuppressedDestination`) and DDB read/write + EventBridge
# PutEvents are already granted (terraform/ses.tf `ses_send`, the role's
# base policy). The only new grant is SQS consume on the new main queue.
# No outbound paid call → no cost cap needed (suppression-list and
# DynamoDB writes are free control-plane ops).
# ---------------------------------------------------------------------------

data "archive_file" "ses_feedback" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/ses-feedback/bootstrap"
  output_path = "${path.module}/.terraform/ses-feedback.zip"
}

resource "aws_lambda_function" "ses_feedback" {
  filename         = data.archive_file.ses_feedback.output_path
  function_name    = "ai-website-agency-ses-feedback${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.ses_feedback.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 30 # suppression-list PutItem + DDB I/O + EventBridge
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

  depends_on = [aws_cloudwatch_log_group.ses_feedback]

  tags = local.common_tags
}

resource "aws_sqs_queue" "ses_feedback_main" {
  name                       = "ai-website-agency-ses-feedback-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 60     # > Lambda timeout (30s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["ses-feedback"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "ses-feedback-main"
  })
}

resource "aws_sqs_queue_policy" "ses_feedback_main" {
  queue_url = aws_sqs_queue.ses_feedback_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "sns.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.ses_feedback_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_sns_topic.ses_feedback.arn
        }
      }
    }]
  })
}

resource "aws_sns_topic_subscription" "ses_feedback_to_sqs" {
  topic_arn            = aws_sns_topic.ses_feedback.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.ses_feedback_main.arn
  raw_message_delivery = false
}

resource "aws_lambda_event_source_mapping" "ses_feedback_sqs" {
  event_source_arn                   = aws_sqs_queue.ses_feedback_main.arn
  function_name                      = aws_lambda_function.ses_feedback.arn
  batch_size                         = 5
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "ses_feedback_sqs_consume" {
  name = "ses-feedback-sqs-consume"
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
      Resource = aws_sqs_queue.ses_feedback_main.arn
    }]
  })
}
