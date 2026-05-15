# ---------------------------------------------------------------------------
# screenshotter Lambda — iter 5.5b. SQS-driven consumer of
# `website.published` events. Mints a short-lived operator-bypass token,
# renders a desktop + mobile screenshot of the preview via the Cloudflare
# Browser Rendering REST API, uploads both PNGs to R2 at
# screenshots/<websiteId>/<size>.png, and patches the Website row's
# `screenshots` map via a targeted UpdateItem.
#
# Per-stage wrappers enforced at handler entry:
#   - killswitch.WithKillSwitch on stages.previewEnabled
#   - idempotency.WithIdempotency keyed on event.eventId
#   - cost.WithCostCap around the Browser Rendering pair (StagePreview)
#
# Credentials:
#   1. AWS (shared lambda_api role) — DDB GetItem/UpdateItem, cost ledger,
#      SQS consume (inline policy below).
#   2. Cloudflare R2 — reuses the publisher's R2 access key/secret SSM
#      params (Object Read & Write covers PutObject).
#   3. Cloudflare Browser Rendering — a dedicated API token (new SSM
#      param) scoped to "Browser Rendering: Edit". Kept separate from the
#      Workers-KV token so each carries only the scope it needs.
#
# No KMS, no EventBridge publish — this Lambda emits no event (the queue
# in iter 6 reads the Website row directly; 03-events.md defines no
# screenshot event).
# ---------------------------------------------------------------------------

data "archive_file" "screenshotter" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/screenshotter/bootstrap"
  output_path = "${path.module}/.terraform/screenshotter.zip"
}

resource "aws_lambda_function" "screenshotter" {
  filename         = data.archive_file.screenshotter.output_path
  function_name    = "ai-website-agency-screenshotter${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.screenshotter.output_base64sha256
  runtime          = "provided.al2023"
  architectures    = ["x86_64"]
  timeout          = 120 # 2 Browser Rendering calls + R2 PUTs + DDB
  memory_size      = 512

  tracing_config {
    mode = "Active"
  }

  # nosemgrep: terraform.aws.security.aws-lambda-environment-unencrypted.aws-lambda-environment-unencrypted
  environment {
    variables = {
      ITEMS_TABLE                        = aws_dynamodb_table.items.name
      ENVIRONMENT                        = var.environment
      LOG_LEVEL                          = local.is_production ? "INFO" : "DEBUG"
      R2_ACCOUNT_ID                      = nonsensitive(data.aws_ssm_parameter.cloudflare_account_id.value)
      R2_BUCKET                          = "ai-website-agency-previews${local.env_suffix}"
      R2_ACCESS_KEY_ID                   = nonsensitive(data.aws_ssm_parameter.r2_access_key_id.value)
      R2_SECRET_ACCESS_KEY               = nonsensitive(data.aws_ssm_parameter.r2_secret_access_key.value)
      CLOUDFLARE_ACCOUNT_ID              = nonsensitive(data.aws_ssm_parameter.cloudflare_account_id.value)
      CLOUDFLARE_BROWSER_RENDERING_TOKEN = nonsensitive(data.aws_ssm_parameter.cloudflare_browser_rendering_token.value)
      PASSCODE_SALT                      = nonsensitive(data.aws_ssm_parameter.passcode_salt.value)
    }
  }

  depends_on = [aws_cloudwatch_log_group.screenshotter]

  tags = local.common_tags
}

resource "aws_sqs_queue" "screenshotter_main" {
  name                       = "ai-website-agency-screenshotter-main${local.env_suffix}"
  message_retention_seconds  = 345600 # 4 days
  visibility_timeout_seconds = 240    # > Lambda timeout (120s)
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq["screenshotter"].arn
    maxReceiveCount     = 3
  })

  tags = merge(local.common_tags, {
    Component = "screenshotter-main"
  })
}

resource "aws_sqs_queue_policy" "screenshotter_main" {
  queue_url = aws_sqs_queue.screenshotter_main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.screenshotter_main.arn
      Condition = {
        ArnEquals = {
          "aws:SourceArn" = aws_cloudwatch_event_rule.website_published.arn
        }
      }
    }]
  })
}

resource "aws_cloudwatch_event_rule" "website_published" {
  name           = "website-published-to-screenshotter${local.env_suffix}"
  description    = "Route website.published events to the screenshotter Lambda"
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name

  event_pattern = jsonencode({
    source        = ["agency.pipeline"]
    "detail-type" = ["website.published"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "website_published_to_screenshotter" {
  rule           = aws_cloudwatch_event_rule.website_published.name
  event_bus_name = aws_cloudwatch_event_bus.pipeline.name
  target_id      = "screenshotter-sqs"
  arn            = aws_sqs_queue.screenshotter_main.arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }

  dead_letter_config {
    arn = aws_sqs_queue.dlq["screenshotter"].arn
  }
}

resource "aws_lambda_event_source_mapping" "screenshotter_sqs" {
  event_source_arn                   = aws_sqs_queue.screenshotter_main.arn
  function_name                      = aws_lambda_function.screenshotter.arn
  batch_size                         = 3
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]
}

resource "aws_iam_role_policy" "screenshotter_sqs_consume" {
  name = "screenshotter-sqs-consume"
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
      Resource = aws_sqs_queue.screenshotter_main.arn
    }]
  })
}

# ---------------------------------------------------------------------------
# SSM parameter — Cloudflare Browser Rendering API token. Same
# placeholder-then-ignore-changes pattern as publisher.tf's secrets;
# operator populates it out-of-band after first apply with a token scoped
# to "Browser Rendering: Edit". Lives under the existing /cloudflare/*
# path family already on the aws-setup ssm_access allowlist.
# ---------------------------------------------------------------------------

# tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_ssm_parameter" "cloudflare_browser_rendering_token" {
  name        = "/ai-website-agency/${var.environment}/cloudflare/browser_rendering_token"
  description = "Cloudflare API token scoped to Browser Rendering:Edit (screenshotter)."
  type        = "SecureString"
  value       = "PLACEHOLDER_SET_OUT_OF_BAND"
  tags        = local.common_tags
  lifecycle {
    ignore_changes = [value]
  }
}

data "aws_ssm_parameter" "cloudflare_browser_rendering_token" {
  name            = aws_ssm_parameter.cloudflare_browser_rendering_token.name
  with_decryption = true
}
