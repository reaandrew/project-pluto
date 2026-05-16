# ---------------------------------------------------------------------------
# passcode-cleanup Lambda — iter 8.5.
#
# Hourly EventBridge Scheduler sweep. The sender (iter 8.2) stamps
# Website.passcodeCleanupDueAt = sentAt + 24h on send; this sweep finds
# Websites whose due time has passed and that still carry a
# passcodeCipher, REMOVEs the KMS-encrypted cleartext (+ the due
# marker), and publishes preview.passcode.cleartext_wiped. The
# Cloudflare KV mapping + passcodeHash are untouched, so the
# recipient's link keeps working.
#
# Reuses the shared lambda_api IAM role (DDB Scan/UpdateItem +
# EventBridge PutEvents already granted) and the scheduler_invoke role
# (scoped to ai-website-agency-*<env_suffix>) from eventbridge.tf. No
# paid call → no cost cap. ENABLED on every env: it is a cheap no-op
# when nothing is due. iter 8.6 extends the same sweep with the
# passcodeRevealableUntil backstop.
# ---------------------------------------------------------------------------

data "archive_file" "passcode_cleanup" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/passcode-cleanup/bootstrap"
  output_path = "${path.module}/.terraform/passcode-cleanup.zip"
}

resource "aws_lambda_function" "passcode_cleanup" {
  filename         = data.archive_file.passcode_cleanup.output_path
  function_name    = "ai-website-agency-passcode-cleanup${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.passcode_cleanup.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 120 # table scan + per-item conditional updates
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

  depends_on = [aws_cloudwatch_log_group.passcode_cleanup]

  tags = local.common_tags
}

resource "aws_scheduler_schedule" "passcode_cleanup_hourly" {
  name       = "passcode-cleanup-hourly"
  group_name = aws_scheduler_schedule_group.pipeline.name
  state      = "ENABLED"

  schedule_expression          = "rate(1 hour)"
  schedule_expression_timezone = "UTC"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_lambda_function.passcode_cleanup.arn
    role_arn = aws_iam_role.scheduler_invoke.arn

    retry_policy {
      maximum_event_age_in_seconds = 3600
      maximum_retry_attempts       = 3
    }
  }
}
