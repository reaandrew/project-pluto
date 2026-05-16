# ---------------------------------------------------------------------------
# Operational alarms — iter 8.3. SES account-level reputation guardrails.
#
# AWS suspends a sending account whose bounce rate crosses ~10% or
# complaint rate ~0.5%. These alarms fire well below those review
# thresholds (bounce 5%, complaint 0.1% — AWS's own "investigate now"
# levels) so an operator can pause outreach (stages.outreachEnabled
# kill switch) before SES does it for us.
#
# `AWS/SES` `Reputation.BounceRate` / `Reputation.ComplaintRate` are
# account-level metrics with NO dimensions (a per-configuration-set
# breakdown would need a separate CloudWatch event destination). They
# are therefore singletons like the SES domain identity — declared
# production-only so preview envs don't each stamp a duplicate alarm on
# the same shared account metric. The 24h period matches the spec's
# "bounce rate ≥5% over 24h".
#
# Subscriptions to the ops topic are a manual operator action (email /
# PagerDuty / Slack) — intentionally not codified per-env.
# ---------------------------------------------------------------------------

resource "aws_sns_topic" "ops_alerts" {
  count = local.is_production ? 1 : 0

  name = "ai-website-agency-ops-alerts${local.env_suffix}"
  tags = local.common_tags
}

resource "aws_cloudwatch_metric_alarm" "ses_bounce_rate" {
  count = local.is_production ? 1 : 0

  alarm_name          = "ai-website-agency-ses-bounce-rate${local.env_suffix}"
  alarm_description   = "SES account bounce rate ≥5% over 24h — pause outreach (stages.outreachEnabled) before AWS review."
  namespace           = "AWS/SES"
  metric_name         = "Reputation.BounceRate"
  statistic           = "Maximum"
  period              = 86400 # 24h
  evaluation_periods  = 1
  threshold           = 0.05
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"

  alarm_actions = [aws_sns_topic.ops_alerts[0].arn]
  ok_actions    = [aws_sns_topic.ops_alerts[0].arn]

  tags = local.common_tags
}

resource "aws_cloudwatch_metric_alarm" "ses_complaint_rate" {
  count = local.is_production ? 1 : 0

  alarm_name          = "ai-website-agency-ses-complaint-rate${local.env_suffix}"
  alarm_description   = "SES account complaint rate ≥0.1% over 24h — pause outreach before AWS review."
  namespace           = "AWS/SES"
  metric_name         = "Reputation.ComplaintRate"
  statistic           = "Maximum"
  period              = 86400 # 24h
  evaluation_periods  = 1
  threshold           = 0.001
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"

  alarm_actions = [aws_sns_topic.ops_alerts[0].arn]
  ok_actions    = [aws_sns_topic.ops_alerts[0].arn]

  tags = local.common_tags
}

output "ops_alerts_topic_arn" {
  value       = local.is_production ? aws_sns_topic.ops_alerts[0].arn : ""
  description = "SNS topic for operational alarms (SES reputation). Subscribe operators manually."
}
