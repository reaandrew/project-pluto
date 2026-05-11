# ---------------------------------------------------------------------------
# discover Lambda — iter 1.3. EventBridge Scheduler fires this hourly (see
# aws_scheduler_schedule.discover_hourly in eventbridge.tf, now ENABLED).
#
# Per-stage wrappers (all enforced at handler entry in lambdas/discover/main.go):
#
#   - killswitch.WithKillSwitch on stages.discoveryEnabled
#   - cost.WithCostCap around the Google Places provider
#   - per-domain dedup via gsi3 query
#
# Reuses the shared lambda_api IAM role — DDB read/write + SSM read +
# events:PutEvents are all already granted to it.
# ---------------------------------------------------------------------------

data "archive_file" "discover" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/discover/bootstrap"
  output_path = "${path.module}/.terraform/discover.zip"
}

resource "aws_lambda_function" "discover" {
  filename         = data.archive_file.discover.output_path
  function_name    = "ai-website-agency-discover${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.discover.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 300 # large to cover the slowest provider; killswitch + cap rein in spend
  memory_size      = 512

  tracing_config {
    mode = "Active"
  }

  # nosemgrep: terraform.aws.security.aws-lambda-environment-unencrypted.aws-lambda-environment-unencrypted
  environment {
    variables = {
      ITEMS_TABLE             = aws_dynamodb_table.items.name
      ENVIRONMENT             = var.environment
      LOG_LEVEL               = local.is_production ? "INFO" : "DEBUG"
      EVENT_BUS_NAME          = aws_cloudwatch_event_bus.pipeline.name
      CSV_BUCKET              = aws_s3_bucket.pipeline_blobs.bucket
      CSV_KEY                 = "discovery/inbox/current.csv"
      COMPANIES_HOUSE_API_KEY = nonsensitive(data.aws_ssm_parameter.companies_house_api_key.value)
      GOOGLE_PLACES_API_KEY   = nonsensitive(data.aws_ssm_parameter.google_places_api_key.value)
    }
  }

  depends_on = [aws_cloudwatch_log_group.discover]

  tags = local.common_tags
}

resource "aws_lambda_permission" "discover_scheduler" {
  statement_id  = "AllowSchedulerInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.discover.function_name
  principal     = "scheduler.amazonaws.com"
  source_arn    = aws_scheduler_schedule.discover_hourly.arn
}

# ---------------------------------------------------------------------------
# Provider API keys — operators populate the SSM parameters out-of-band
# (Companies House: https://developer.company-information.service.gov.uk/;
# Google Places: GCP console). Terraform reads the values at apply time so
# the Lambda's env vars carry the latest credentials without a redeploy.
#
# Pitfall #4: the SSM parameters are seeded with a placeholder value the
# first time the stack applies; operators update them via the AWS Console
# afterwards. `lifecycle.ignore_changes=[value]` keeps Terraform from
# clobbering an operator-supplied real key on subsequent applies.
# ---------------------------------------------------------------------------

# tfsec:ignore:aws-ssm-secret-use-customer-key  -- AWS-managed key is fine here; rotation handled by the SSM service.
resource "aws_ssm_parameter" "companies_house_api_key" {
  name        = "/ai-website-agency/${var.environment}/providers/companies_house_api_key"
  description = "Companies House Search Companies API key. Operator-supplied; replace the placeholder after first apply."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags

  lifecycle {
    ignore_changes = [value]
  }
}

# tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_ssm_parameter" "google_places_api_key" {
  name        = "/ai-website-agency/${var.environment}/providers/google_places_api_key"
  description = "Google Places API (New) key. Operator-supplied; replace the placeholder after first apply."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags

  lifecycle {
    ignore_changes = [value]
  }
}

# Read the current value at apply time so the Lambda env var carries the
# operator-supplied key, not the placeholder.
data "aws_ssm_parameter" "companies_house_api_key" {
  name            = aws_ssm_parameter.companies_house_api_key.name
  with_decryption = true
}

data "aws_ssm_parameter" "google_places_api_key" {
  name            = aws_ssm_parameter.google_places_api_key.name
  with_decryption = true
}
