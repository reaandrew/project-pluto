# ---------------------------------------------------------------------------
# reply-detector Lambda — iter 8.4. SES inbound receipt rule stores
# replies to the outreach domain in S3; an S3 ObjectCreated
# notification invokes this function, which attributes the reply via
# the plus-addressed Reply-To token (= draftId) → REPLYREF# index,
# flips Business.status to "responded", writes EmailEvent(replied) and
# publishes email.replied.
#
# Singleton parts (SES receipt rule SET is an account-level singleton —
# only one active rule set per account/region; the receiving MX is a
# domain singleton) are production-only, mirroring the SES sending
# identity in ses.tf. Preview envs get the per-env bucket + Lambda
# (harmless, just never receives mail). Inbound mail is personal data:
# the bucket is private + SSE + short-lifecycle, replies are never
# logged.
# ---------------------------------------------------------------------------

resource "aws_s3_bucket" "inbound_mail" {
  bucket        = "ai-website-agency-inbound${local.env_suffix}-${data.aws_caller_identity.current.account_id}"
  force_destroy = !local.is_production

  tags = merge(local.common_tags, {
    Name      = "ai-website-agency-inbound${local.env_suffix}"
    Component = "inbound-mail"
  })
}

resource "aws_s3_bucket_server_side_encryption_configuration" "inbound_mail" {
  bucket = aws_s3_bucket.inbound_mail.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "inbound_mail" {
  bucket                  = aws_s3_bucket.inbound_mail.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Replies are personal data — keep the minimum needed for triage
# (iter 8.5) + manual inspection, then expire.
resource "aws_s3_bucket_lifecycle_configuration" "inbound_mail" {
  bucket = aws_s3_bucket.inbound_mail.id
  rule {
    id     = "expire-inbound"
    status = "Enabled"
    filter {}
    expiration {
      days = 90
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

# SES receipt-rule S3 action writes objects here. Scope to this
# account + the specific receipt rule (modern SourceAccount/SourceArn
# conditions rather than the legacy aws:Referer).
resource "aws_s3_bucket_policy" "inbound_mail" {
  count  = local.is_production ? 1 : 0
  bucket = aws_s3_bucket.inbound_mail.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ses.amazonaws.com" }
      Action    = "s3:PutObject"
      Resource  = "${aws_s3_bucket.inbound_mail.arn}/inbound/*"
      Condition = {
        StringEquals = { "aws:SourceAccount" = data.aws_caller_identity.current.account_id }
        StringLike   = { "aws:SourceArn" = "arn:aws:ses:${var.aws_region}:${data.aws_caller_identity.current.account_id}:receipt-rule-set/${aws_ses_receipt_rule_set.outreach[0].rule_set_name}:receipt-rule/*" }
      }
    }]
  })
}

data "archive_file" "reply_detector" {
  type        = "zip"
  source_file = "${path.module}/../lambdas/reply-detector/bootstrap"
  output_path = "${path.module}/.terraform/reply-detector.zip"
}

resource "aws_lambda_function" "reply_detector" {
  filename         = data.archive_file.reply_detector.output_path
  function_name    = "ai-website-agency-reply-detector${local.env_suffix}"
  role             = aws_iam_role.lambda_api.arn
  handler          = "bootstrap"
  source_code_hash = data.archive_file.reply_detector.output_base64sha256
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
      ITEMS_TABLE    = aws_dynamodb_table.items.name
      ENVIRONMENT    = var.environment
      LOG_LEVEL      = local.is_production ? "INFO" : "DEBUG"
      EVENT_BUS_NAME = aws_cloudwatch_event_bus.pipeline.name
      REPLY_DOMAIN   = "outreach.${var.base_domain}"
    }
  }

  depends_on = [aws_cloudwatch_log_group.reply_detector]

  tags = local.common_tags
}

# A reply is not silently lost without an explicit DLQ: the async S3
# invoke retries twice by default, and the raw object persists in the
# inbound bucket for the full 90-day lifecycle, so a hard-failed reply
# can always be re-driven / picked up by iter-8.5 triage from S3. An
# on-failure destination would need `lambda:PutFunctionEventInvokeConfig`
# on the CI deploy role (defined in the do-not-touch aws-setup/
# singleton) — out of scope for iter 8.4.
# Fan-out via EventBridge. S3 forbids two notification destinations
# with the same prefix + event type ("Configuration is ambiguously
# defined") — so the inbound bucket emits to EventBridge instead and a
# single rule fans the "Object Created" event to BOTH consumers
# (reply-detector iter 8.4 + reply-triage iter 8.5.1). EventBridge has
# no overlap restriction and is the idiomatic multi-consumer pattern.
resource "aws_s3_bucket_notification" "inbound_mail" {
  bucket      = aws_s3_bucket.inbound_mail.id
  eventbridge = true
}

resource "aws_cloudwatch_event_rule" "inbound_mail" {
  name        = "ai-website-agency-inbound-mail${local.env_suffix}"
  description = "SES inbound replies landed in S3 → reply-detector + reply-triage"

  event_pattern = jsonencode({
    source        = ["aws.s3"]
    "detail-type" = ["Object Created"]
    detail = {
      bucket = { name = [aws_s3_bucket.inbound_mail.id] }
      object = { key = [{ prefix = "inbound/" }] }
    }
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "inbound_mail_detector" {
  rule      = aws_cloudwatch_event_rule.inbound_mail.name
  target_id = "reply-detector"
  arn       = aws_lambda_function.reply_detector.arn
}

resource "aws_cloudwatch_event_target" "inbound_mail_triage" {
  rule      = aws_cloudwatch_event_rule.inbound_mail.name
  target_id = "reply-triage"
  arn       = aws_lambda_function.reply_triage.arn
}

resource "aws_lambda_permission" "reply_detector_eb" {
  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.reply_detector.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.inbound_mail.arn
}

resource "aws_lambda_permission" "reply_triage_eb" {
  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.reply_triage.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.inbound_mail.arn
}

# Scoped read on the inbound bucket. DynamoDB rw + EventBridge
# PutEvents are already on the shared lambda_api role.
resource "aws_iam_role_policy" "reply_detector_s3_read" {
  name = "reply-detector-s3-read"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = "s3:GetObject"
        Resource = "${aws_s3_bucket.inbound_mail.arn}/inbound/*"
      },
    ]
  })
}

# --- SES inbound (production-only account/domain singletons) -----------

resource "aws_ses_receipt_rule_set" "outreach" {
  count         = local.is_production ? 1 : 0
  rule_set_name = "ai-website-agency-inbound${local.env_suffix}"
}

resource "aws_ses_active_receipt_rule_set" "outreach" {
  count         = local.is_production ? 1 : 0
  rule_set_name = aws_ses_receipt_rule_set.outreach[0].rule_set_name
}

resource "aws_ses_receipt_rule" "outreach_replies" {
  count         = local.is_production ? 1 : 0
  name          = "store-replies-to-s3"
  rule_set_name = aws_ses_receipt_rule_set.outreach[0].rule_set_name
  recipients    = ["outreach.${var.base_domain}"]
  enabled       = true
  scan_enabled  = true
  tls_policy    = "Require"

  s3_action {
    bucket_name       = aws_s3_bucket.inbound_mail.id
    object_key_prefix = "inbound/"
    position          = 1
  }

  depends_on = [aws_s3_bucket_policy.inbound_mail]
}

# Receiving MX for the outreach domain → SES inbound endpoint. Domain
# singleton (same reasoning as the DKIM/MAIL-FROM records in ses.tf);
# reuses the route53 zone-id SSM param declared there.
resource "aws_route53_record" "outreach_inbound_mx" {
  count = local.is_production ? 1 : 0

  zone_id = data.aws_ssm_parameter.route53_zone_id.value
  name    = "outreach.${var.base_domain}"
  type    = "MX"
  ttl     = 600
  records = ["10 inbound-smtp.${var.aws_region}.amazonaws.com"]
}

output "inbound_mail_bucket" {
  value       = aws_s3_bucket.inbound_mail.id
  description = "S3 bucket where SES stores inbound replies (iter 8.4 reply-detector source; iter 8.5 reply-triage reuses it)"
}
