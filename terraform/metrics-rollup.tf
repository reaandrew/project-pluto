# ---------------------------------------------------------------------------
# metrics-rollup Lambda — iter 11.1. Hourly EventBridge-scheduled job
# that snapshots the funnel (Business count per status via gsi1 COUNT)
# + the day's cost ledger and writes a Metric item (METRIC#<date>
# ROLLUP, overwritten each run). /metrics (11.2/11.3) reads these.
#
# Reuses the shared lambda_api role (DDB read/write already granted)
# and the scheduler_invoke role (eventbridge.tf). No new IAM, no paid
# call, not kill-switch gated, idempotent (the date row is overwritten).
# Same proven scheduled pattern as cost-rollover/passcode-cleanup.
# ---------------------------------------------------------------------------

data "archive_file" "metrics_rollup" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/metrics-rollup/bootstrap"
  output_path = "${path.module}/.terraform/metrics-rollup.zip"
}

resource "aws_lambda_function" "metrics_rollup" {
  filename         = data.archive_file.metrics_rollup.output_path
  function_name    = "ai-website-agency-metrics-rollup${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.metrics_rollup.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 120 # 12 gsi1 COUNT queries + 1 cost query + 1 put
  memory_size      = 256

  tracing_config {
    mode = "Active"
  }

  # nosemgrep: terraform.aws.security.aws-lambda-environment-unencrypted.aws-lambda-environment-unencrypted
  environment {
    variables = {
      ITEMS_TABLE = aws_dynamodb_table.items.name
      ENVIRONMENT = var.environment
      LOG_LEVEL   = local.is_production ? "INFO" : "DEBUG"
    }
  }

  depends_on = [aws_cloudwatch_log_group.metrics_rollup]

  tags = local.common_tags
}

# nosemgrep: terraform.aws.security.aws-cloudwatch-log-group-unencrypted.aws-cloudwatch-log-group-unencrypted
resource "aws_cloudwatch_log_group" "metrics_rollup" {
  name              = "/aws/lambda/ai-website-agency-metrics-rollup${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}

resource "aws_scheduler_schedule" "metrics_rollup_hourly" {
  name       = "metrics-rollup-hourly"
  group_name = aws_scheduler_schedule_group.pipeline.name
  state      = "ENABLED"

  schedule_expression          = "rate(1 hour)"
  schedule_expression_timezone = "UTC"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_lambda_function.metrics_rollup.arn
    role_arn = aws_iam_role.scheduler_invoke.arn

    retry_policy {
      maximum_event_age_in_seconds = 3600
      maximum_retry_attempts       = 3
    }
  }
}
