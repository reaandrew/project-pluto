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

# Single S3→Lambda notification (reply-detector only) — the iter-8.4
# wiring, proven to deploy. Direct S3 fan-out to a second consumer is
# impossible (S3 rejects two destinations sharing a prefix+event) and
# the CI deploy role (aws-setup/, do-not-touch) cannot create
# default-bus EventBridge rules (events:PutRule/PutTargets/TagResource
# denied off the pipeline bus). So reply-triage (iter 8.5.1) is NOT
# triggered here — reply-detector republishes a `reply.received`
# pipeline event that reply-triage consumes via SQS (see
# reply-triage.tf), the project's standard async-consumer pattern.
#
# No on-failure DLQ: S3 async-retries and the raw object persists in
# the bucket for the 90d lifecycle (lambda:PutFunctionEventInvokeConfig
# is also denied to the CI role — iter 8.4 precedent).
resource "aws_lambda_permission" "reply_detector_s3" {
  statement_id   = "AllowS3Invoke"
  action         = "lambda:InvokeFunction"
  function_name  = aws_lambda_function.reply_detector.function_name
  principal      = "s3.amazonaws.com"
  source_arn     = aws_s3_bucket.inbound_mail.arn
  source_account = data.aws_caller_identity.current.account_id
}

resource "aws_s3_bucket_notification" "inbound_mail" {
  bucket = aws_s3_bucket.inbound_mail.id

  lambda_function {
    lambda_function_arn = aws_lambda_function.reply_detector.arn
    events              = ["s3:ObjectCreated:*"]
    filter_prefix       = "inbound/"
  }

  depends_on = [aws_lambda_permission.reply_detector_s3]
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
