# ---------------------------------------------------------------------------
# Weekly tuner Lambdas — iter 9.3. Each reads the last 7 days of
# operator Feedback per vertical, asks its tuner.*.v1 Bedrock prompt
# for a conservative profile delta, and writes a PENDING TunerDelta +
# publishes tuner.delta.proposed. Nothing is auto-applied (iter 9.4).
#
# Triggered by the Sunday 02:0x UTC EventBridge Scheduler rules in
# eventbridge.tf (enabled here in iter 9.3 by flipping their `state`).
# Those rules target the function ARNs by string via the
# scheduler_invoke role — no per-rule IAM here.
#
# Reuse the shared lambda_api role: DDB read/write (Feedback query,
# TunerDelta put, profile get), EventBridge PutEvents, and Bedrock
# invoke (bedrock-iam.tf) are all already granted. NOT kill-switch
# gated; the paid Bedrock call is cost-capped from the Bedrock budget
# inside pkg/tunerlib. No on-failure config (CI role lacks
# PutFunctionEventInvokeConfig — iter 8.4); the weekly schedule's own
# retry_policy (3 attempts) covers a transient failure.
# ---------------------------------------------------------------------------

locals {
  tuners = {
    "tuner-targeting"  = "tuner_targeting"
    "tuner-style"      = "tuner_style"
    "tuner-email-tone" = "tuner_email_tone"
  }
}

data "archive_file" "tuner" {
  for_each    = local.tuners
  type        = "zip"
  source_file = "${path.module}/../lambdas/${each.key}/bootstrap"
  output_path = "${path.module}/.terraform/${each.key}.zip"
}

resource "aws_lambda_function" "tuner" {
  for_each = local.tuners

  filename         = data.archive_file.tuner[each.key].output_path
  function_name    = "ai-website-agency-${each.key}${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.tuner[each.key].output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 300 # week of feedback + a Sonnet/Haiku call per vertical
  memory_size      = 512

  tracing_config {
    mode = "Active"
  }

  # nosemgrep: terraform.aws.security.aws-lambda-environment-unencrypted.aws-lambda-environment-unencrypted
  environment {
    variables = {
      ITEMS_TABLE     = aws_dynamodb_table.items.name
      ENVIRONMENT     = var.environment
      LOG_LEVEL       = local.is_production ? "INFO" : "DEBUG"
      EVENT_BUS_NAME  = aws_cloudwatch_event_bus.pipeline.name
      TUNER_VERTICALS = "default,accountants" # the seeded style/tone profiles
    }
  }

  depends_on = [aws_cloudwatch_log_group.tuner]

  tags = local.common_tags
}

# nosemgrep: terraform.aws.security.aws-cloudwatch-log-group-unencrypted.aws-cloudwatch-log-group-unencrypted
resource "aws_cloudwatch_log_group" "tuner" {
  for_each = local.tuners

  name              = "/aws/lambda/ai-website-agency-${each.key}${local.env_suffix}"
  retention_in_days = 30
  tags              = local.common_tags
}
