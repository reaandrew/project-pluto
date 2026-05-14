# ---------------------------------------------------------------------------
# publisher Lambda — iter 5.3. SQS-driven consumer of `website.generated`
# events. Copies the generated HTML from S3 → Cloudflare R2 at
# sites/<websiteId>/index.html, issues an 8-char passcode (hash to KV,
# KMS-encrypt cleartext into Website.passcodeCipher), updates the
# Website row, publishes website.published (no cleartext).
#
# Per-stage wrappers enforced at handler entry:
#   - killswitch.WithKillSwitch on stages.previewEnabled
#   - idempotency.WithIdempotency keyed on event.eventId
#
# The Lambda needs THREE sets of credentials:
#   1. AWS (default Lambda execution role)  — DDB, S3 (blobs bucket),
#      KMS Encrypt, EventBridge PutEvents (all on the shared role).
#   2. Cloudflare R2 — separate access key + secret (R2 API tokens
#      scoped to "Object Read & Write"). Stored in two SSM params.
#   3. Cloudflare API — for Workers KV writes. One SSM param.
#
# The KMS key policy already grants this role kms:Encrypt; no extra
# identity policy needed (see terraform/kms.tf).
# ---------------------------------------------------------------------------

data "archive_file" "publisher" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/publisher/bootstrap"
  output_path = "${path.module}/.terraform/publisher.zip"
}

resource "aws_lambda_function" "publisher" {
  filename         = data.archive_file.publisher.output_path
  function_name    = "ai-website-agency-publisher${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.publisher.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 90 # S3 read + R2 PUT + KMS encrypt + KV write + DDB write
  memory_size      = 512

  tracing_config {
    mode = "Active"
  }

  # nosemgrep: terraform.aws.security.aws-lambda-environment-unencrypted.aws-lambda-environment-unencrypted
  environment {
    variables = {
      ITEMS_TABLE                = aws_dynamodb_table.items.name
      ENVIRONMENT                = var.environment
      LOG_LEVEL                  = local.is_production ? "INFO" : "DEBUG"
      EVENT_BUS_NAME             = aws_cloudwatch_event_bus.pipeline.name
      BLOBS_BUCKET               = aws_s3_bucket.pipeline_blobs.bucket
      PASSCODE_KMS_KEY_ID        = aws_kms_alias.passcode_cleartext.target_key_arn
      PASSCODE_SALT              = nonsensitive(data.aws_ssm_parameter.passcode_salt.value)
      R2_ACCOUNT_ID              = nonsensitive(data.aws_ssm_parameter.cloudflare_account_id.value)
      R2_BUCKET                  = "ai-website-agency-previews${local.env_suffix}"
      R2_ACCESS_KEY_ID           = nonsensitive(data.aws_ssm_parameter.r2_access_key_id.value)
      R2_SECRET_ACCESS_KEY       = nonsensitive(data.aws_ssm_parameter.r2_secret_access_key.value)
      CLOUDFLARE_ACCOUNT_ID      = nonsensitive(data.aws_ssm_parameter.cloudflare_account_id.value)
      CLOUDFLARE_KV_NAMESPACE_ID = nonsensitive(data.aws_ssm_parameter.cloudflare_kv_namespace_id.value)
      CLOUDFLARE_API_TOKEN       = nonsensitive(data.aws_ssm_parameter.cloudflare_api_token.value)
      PREVIEW_URL_BASE           = nonsensitive(data.aws_ssm_parameter.preview_url_base.value)
    }
  }

  depends_on = [aws_cloudwatch_log_group.publisher]

  tags = local.common_tags
}

resource "aws_sqs_queue" "publisher_main" {
  name                       = "ai-website-agency-publisher-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 180    # > Lambda timeout (90s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["publisher"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "publisher-main"
  })
}

resource "aws_sqs_queue_policy" "publisher_main" {
  queue_url = aws_sqs_queue.publisher_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.publisher_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_cloudwatch_event_rule.website_generated.arn
        }
      }
    }]
  })
}

resource "aws_cloudwatch_event_rule" "website_generated" {
  name           = "website-generated-to-publisher${local.env_suffix}"
  description    = "Route website.generated events to the publisher Lambda"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["website.generated"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "website_generated_to_publisher" {
  rule           = aws_cloudwatch_event_rule.website_generated.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "publisher-sqs"
  arn            = aws_sqs_queue.publisher_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["publisher"].arn
  }
}

resource "aws_lambda_event_source_mapping" "publisher_sqs" {
  event_source_arn                   = aws_sqs_queue.publisher_main.arn
  function_name                      = aws_lambda_function.publisher.arn
  batch_size                         = 3
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "publisher_sqs_consume" {
  name = "publisher-sqs-consume"
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
      Resource = aws_sqs_queue.publisher_main.arn
    }]
  })
}

# ---------------------------------------------------------------------------
# SSM parameters — operator-supplied secrets (R2 + Cloudflare API token,
# KV namespace ID, passcode salt, preview URL base). Same
# placeholder-then-ignore-changes pattern as discover.tf's providers.
# ---------------------------------------------------------------------------

# tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_ssm_parameter" "r2_access_key_id" {
  name        = "/ai-website-agency/${var.environment}/r2/access_key_id"
  description = "Cloudflare R2 access key ID (Object Read & Write scope). Operator-supplied."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags
  lifecycle {
    ignore_changes = [value]
  }
}

# tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_ssm_parameter" "r2_secret_access_key" {
  name        = "/ai-website-agency/${var.environment}/r2/secret_access_key"
  description = "Cloudflare R2 secret access key. Operator-supplied."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags
  lifecycle {
    ignore_changes = [value]
  }
}

# tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_ssm_parameter" "cloudflare_account_id" {
  name        = "/ai-website-agency/${var.environment}/cloudflare/account_id"
  description = "Cloudflare account UUID (also used as R2 endpoint subdomain)."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags
  lifecycle {
    ignore_changes = [value]
  }
}

# tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_ssm_parameter" "cloudflare_kv_namespace_id" {
  name        = "/ai-website-agency/${var.environment}/cloudflare/kv_namespace_id"
  description = "Workers KV namespace ID (terraform output -raw kv_namespace_id from cloudflare/ stack)."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags
  lifecycle {
    ignore_changes = [value]
  }
}

# tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_ssm_parameter" "cloudflare_api_token" {
  name        = "/ai-website-agency/${var.environment}/cloudflare/api_token"
  description = "Cloudflare API token with Workers KV Storage:Edit scope (publisher writes hashes)."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags
  lifecycle {
    ignore_changes = [value]
  }
}

# tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_ssm_parameter" "passcode_salt" {
  name        = "/ai-website-agency/${var.environment}/passcode/salt"
  description = "Salt for SHA-256 passcode hash (cross-checked with worker/src/passcode.ts)."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags
  lifecycle {
    ignore_changes = [value]
  }
}

# tfsec:ignore:aws-ssm-secret-use-customer-key  -- not a secret; SecureString for consistency with the rest of the param tree.
resource "aws_ssm_parameter" "preview_url_base" {
  name        = "/ai-website-agency/${var.environment}/preview/url_base"
  description = "Public preview URL base (e.g. https://previews.agency.techar.ch or *.workers.dev fallback)."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags
  lifecycle {
    ignore_changes = [value]
  }
}

# Read the current values at apply time so the Lambda's env vars carry
# the operator-supplied values, not the placeholders.
data "aws_ssm_parameter" "r2_access_key_id" {
  name            = aws_ssm_parameter.r2_access_key_id.name
  with_decryption = true
}
data "aws_ssm_parameter" "r2_secret_access_key" {
  name            = aws_ssm_parameter.r2_secret_access_key.name
  with_decryption = true
}
data "aws_ssm_parameter" "cloudflare_account_id" {
  name            = aws_ssm_parameter.cloudflare_account_id.name
  with_decryption = true
}
data "aws_ssm_parameter" "cloudflare_kv_namespace_id" {
  name            = aws_ssm_parameter.cloudflare_kv_namespace_id.name
  with_decryption = true
}
data "aws_ssm_parameter" "cloudflare_api_token" {
  name            = aws_ssm_parameter.cloudflare_api_token.name
  with_decryption = true
}
data "aws_ssm_parameter" "passcode_salt" {
  name            = aws_ssm_parameter.passcode_salt.name
  with_decryption = true
}
data "aws_ssm_parameter" "preview_url_base" {
  name            = aws_ssm_parameter.preview_url_base.name
  with_decryption = true
}
