# SES setup for outbound emails. Sender = `outreach.<base_domain>` (e.g.
# outreach.agency.techar.ch). Per `.ralph/specs/03-events.md` (`email.sent`)
# and iter 8 (`lambdas/sender/`).
#
# Domain verification (DKIM CNAMEs, MAIL FROM, SPF) ships separately as a
# manual one-shot in `docs/BOOTSTRAP.md` step 7-bis (added when iter 8 lands).

# Configuration set: routes events (deliveries, bounces, complaints, opens) to SNS.
resource "aws_sesv2_configuration_set" "outreach" {
  configuration_set_name = "ai-website-agency-outreach${local.env_suffix}"

  delivery_options {
    tls_policy = "REQUIRE"
  }

  reputation_options {
    reputation_metrics_enabled = true
  }

  sending_options {
    sending_enabled = true
  }

  suppression_options {
    suppressed_reasons = ["BOUNCE", "COMPLAINT"]
  }

  tags = local.common_tags
}

# Domain identity + DKIM/MX/SPF DNS records are domain-level **singletons** —
# there's only one `outreach.<base_domain>` for the whole project. They live in
# the production env (skeleton pitfall #10 spirit: per-domain resources can't be
# per-env or every preview env's apply collides on the same DNS record name).
# Preview envs reuse the production-registered identity at SES SendEmail time;
# they have their own configuration set + SNS topic but no identity object.

resource "aws_sesv2_email_identity" "outreach_domain" {
  count = local.is_production ? 1 : 0

  email_identity         = "outreach.${var.base_domain}"
  configuration_set_name = aws_sesv2_configuration_set.outreach.configuration_set_name
  tags                   = local.common_tags
}

resource "aws_sesv2_email_identity_mail_from_attributes" "outreach_domain" {
  count = local.is_production ? 1 : 0

  email_identity         = aws_sesv2_email_identity.outreach_domain[0].email_identity
  mail_from_domain       = "mail.outreach.${var.base_domain}"
  behavior_on_mx_failure = "REJECT_MESSAGE"
  depends_on             = [aws_route53_record.ses_mail_from_mx, aws_route53_record.ses_mail_from_spf]
}

# DKIM CNAMEs in the project's hosted zone (the agency.<parent> zone created in
# aws-setup/). Production-only because the records are domain-singletons.
data "aws_ssm_parameter" "route53_zone_id" {
  name = "/ai-website-agency/route53/zone_id"
}

resource "aws_route53_record" "ses_dkim" {
  count = local.is_production ? 3 : 0

  zone_id = data.aws_ssm_parameter.route53_zone_id.value
  name    = "${aws_sesv2_email_identity.outreach_domain[0].dkim_signing_attributes[0].tokens[count.index]}._domainkey.outreach.${var.base_domain}"
  type    = "CNAME"
  ttl     = 600
  records = ["${aws_sesv2_email_identity.outreach_domain[0].dkim_signing_attributes[0].tokens[count.index]}.dkim.amazonses.com"]
}

# MX + SPF for the MAIL FROM domain (production-only; same singleton reasoning).
resource "aws_route53_record" "ses_mail_from_mx" {
  count = local.is_production ? 1 : 0

  zone_id = data.aws_ssm_parameter.route53_zone_id.value
  name    = "mail.outreach.${var.base_domain}"
  type    = "MX"
  ttl     = 600
  records = ["10 feedback-smtp.${var.aws_region}.amazonses.com"]
}

resource "aws_route53_record" "ses_mail_from_spf" {
  count = local.is_production ? 1 : 0

  zone_id = data.aws_ssm_parameter.route53_zone_id.value
  name    = "mail.outreach.${var.base_domain}"
  type    = "TXT"
  ttl     = 600
  records = ["v=spf1 include:amazonses.com -all"]
}

# Bounce / complaint feedback → SNS topic. Iter 8.3 wires a Lambda subscriber
# that updates the suppression list and Business.status.
resource "aws_sns_topic" "ses_feedback" {
  name = "ai-website-agency-ses-feedback${local.env_suffix}"
  tags = local.common_tags
}

resource "aws_sesv2_configuration_set_event_destination" "feedback_sns" {
  configuration_set_name = aws_sesv2_configuration_set.outreach.configuration_set_name
  event_destination_name = "feedback-sns"

  event_destination {
    enabled = true
    matching_event_types = [
      "BOUNCE",
      "COMPLAINT",
      "DELIVERY",
      "REJECT",
      "RENDERING_FAILURE",
    ]

    sns_destination {
      topic_arn = aws_sns_topic.ses_feedback.arn
    }
  }
}

# Allow SES to publish to the SNS topic.
resource "aws_sns_topic_policy" "ses_feedback" {
  arn = aws_sns_topic.ses_feedback.arn
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ses.amazonaws.com" }
      Action    = "SNS:Publish"
      Resource  = aws_sns_topic.ses_feedback.arn
      Condition = {
        StringEquals = { "aws:SourceAccount" = data.aws_caller_identity.current.account_id }
      }
    }]
  })
}

# Lambdas need permission to send through this configuration set. The identity ARN
# is constructed as a static string (production-only resource exists, but every env
# needs the IAM grant so any Lambda can call SendEmail without conditional policies).
resource "aws_iam_role_policy" "ses_send" {
  name = "ses-send"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = ["ses:SendEmail", "ses:SendRawEmail"]
        Resource = [
          # Identity ARN is a global singleton (one outreach.<base_domain> for the project).
          "arn:aws:ses:${var.aws_region}:${data.aws_caller_identity.current.account_id}:identity/outreach.${var.base_domain}",
          "arn:aws:ses:${var.aws_region}:${data.aws_caller_identity.current.account_id}:configuration-set/${aws_sesv2_configuration_set.outreach.configuration_set_name}",
        ]
        Condition = {
          StringEquals = {
            "ses:FromAddress" = "outreach@outreach.${var.base_domain}"
          }
        }
      },
      {
        Effect = "Allow"
        Action = [
          "sesv2:GetSuppressedDestination",
          "sesv2:PutSuppressedDestination",
          "sesv2:DeleteSuppressedDestination",
        ]
        Resource = "*"
      },
    ]
  })
}

output "ses_outreach_identity_arn" {
  value       = "arn:aws:ses:${var.aws_region}:${data.aws_caller_identity.current.account_id}:identity/outreach.${var.base_domain}"
  description = "SES identity ARN for outreach.<base_domain> (production-registered domain singleton)"
}

output "ses_feedback_topic_arn" {
  value       = aws_sns_topic.ses_feedback.arn
  description = "SNS topic for SES bounce/complaint/delivery events (iter 8.3 subscriber target)"
}
