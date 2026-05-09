# ---------------------------------------------------------------------------
# Cost-rollover Lambda — iter 0.F.4.
#
# Fires at 00:05 UTC every day. Reads the singleton PipelineSettings row,
# re-enables any stages whose pause reason is "budget" (set by pkg/cost
# when the daily cap was hit), clears the reason, writes back. Operator-
# disabled stages (no pause reason) are left untouched.
#
# Spend counters reset implicitly because cost records are bucketed by
# `pk=CAP#YYYY-MM-DD` — a new day starts at zero. The 30-day TTL on
# `expires_at` ages prior days out automatically.
#
# Reuses the shared lambda_api IAM role (terraform/iam.tf) — DDB
# read/write on the items table is already granted there.
# ---------------------------------------------------------------------------

data "archive_file" "cost_rollover" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/cost-rollover/bootstrap"
  output_path = "${path.module}/.terraform/cost-rollover.zip"
}

resource "aws_lambda_function" "cost_rollover" {
  filename         = data.archive_file.cost_rollover.output_path
  function_name    = "ai-website-agency-cost-rollover${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.cost_rollover.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 30
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

  depends_on = [aws_cloudwatch_log_group.cost_rollover]

  tags = local.common_tags
}

# Daily 00:05 UTC schedule. The 5-minute offset gives DDB-side metric
# rollups a few minutes of breathing room past midnight before the
# rollover writes — defensive in case clocks ever drift.
#
# This rule is ENABLED on every env (unlike the iter-1+ schedules that
# stay DISABLED until their consumer Lambdas exist). The Lambda is a
# no-op when nothing is paused, so leaving it on costs almost nothing.
resource "aws_scheduler_schedule" "cost_rollover_daily" {
  name       = "cost-rollover-daily"
  group_name = aws_scheduler_schedule_group.pipeline.name
  state      = "ENABLED"

  schedule_expression          = "cron(5 0 * * ? *)"
  schedule_expression_timezone = "UTC"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_lambda_function.cost_rollover.arn
    role_arn = aws_iam_role.scheduler_invoke.arn

    retry_policy {
      maximum_event_age_in_seconds = 3600
      maximum_retry_attempts       = 3
    }
  }
}
