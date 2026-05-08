# Custom EventBridge bus for the agency pipeline. All in-house events flow through
# this bus (source = "agency.pipeline"). Per `.ralph/specs/03-events.md`. The bus is
# **per-env** — never lift to aws-setup/ (skeleton pitfall #10).

resource "aws_cloudwatch_event_bus" "pipeline" {
  name = "pipeline${local.env_suffix}"
  tags = local.common_tags
}

# Archive: 30 days for preview envs, 90 days for production. Used by the weekly
# tuner Lambdas in iter 9 to replay `feedback.captured` events.
resource "aws_cloudwatch_event_archive" "pipeline" {
  name             = "pipeline${local.env_suffix}-archive"
  event_source_arn = aws_cloudwatch_event_bus.pipeline.arn
  retention_days   = local.is_production ? 90 : 30
  description      = "Archive of all events on the pipeline bus (replay source for tuners)"
}

# EventBridge Scheduler group — owns the cron rules that drive the pipeline
# (hourly discovery, weekly tuners). Group lets us tag/IAM-scope all schedules
# together rather than per-rule.
resource "aws_scheduler_schedule_group" "pipeline" {
  name = "pipeline${local.env_suffix}"
  tags = local.common_tags
}

# IAM role assumed by EventBridge Scheduler when it invokes the targets below.
resource "aws_iam_role" "scheduler_invoke" {
  name        = "ai-website-agency-scheduler${local.env_suffix}"
  description = "Assumed by EventBridge Scheduler to invoke pipeline Lambdas (env: ${var.environment})"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "scheduler.amazonaws.com" }
      Action    = "sts:AssumeRole"
      # Confused-deputy mitigation: only schedules within our project's scheduler
      # group (in this account) can assume this role. SourceAccount alone permits
      # any schedule in the account; SourceArn scopes it to the right group.
      Condition = {
        StringEquals = {
          "aws:SourceAccount" = var.aws_account_id
        }
        ArnLike = {
          "aws:SourceArn" = "arn:aws:scheduler:${var.aws_region}:${var.aws_account_id}:schedule/${aws_scheduler_schedule_group.pipeline.name}/*"
        }
      }
    }]
  })

  tags = local.common_tags
}

resource "aws_iam_role_policy" "scheduler_invoke_lambda" {
  name = "invoke-pipeline-lambdas"
  role = aws_iam_role.scheduler_invoke.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = ["lambda:InvokeFunction"]
      # Wildcard scoped to project name + this env so future Lambdas (discover,
      # qualifier, etc.) get scheduled invocation for free as we add them.
      Resource = "arn:aws:lambda:${var.aws_region}:${var.aws_account_id}:function:ai-website-agency-*${local.env_suffix}"
    }]
  })
}

# ---------------------------------------------------------------------------
# Hourly discovery rule — DISABLED at iter 0.C; enabled in iter 1.3 once the
# discover Lambda exists. Defined here so the schedule is in code from day one.
# ---------------------------------------------------------------------------
resource "aws_scheduler_schedule" "discover_hourly" {
  name       = "discover-hourly"
  group_name = aws_scheduler_schedule_group.pipeline.name
  state      = "DISABLED" # iter 1.3 enables

  schedule_expression          = "rate(1 hour)"
  schedule_expression_timezone = "UTC"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    # Placeholder ARN — replaced by the real discover Lambda ARN in iter 1.3.
    arn      = "arn:aws:lambda:${var.aws_region}:${var.aws_account_id}:function:ai-website-agency-discover${local.env_suffix}"
    role_arn = aws_iam_role.scheduler_invoke.arn

    retry_policy {
      maximum_event_age_in_seconds = 3600
      maximum_retry_attempts       = 3
    }
  }
}

# ---------------------------------------------------------------------------
# Weekly tuner rules — Sunday 02:00 UTC. DISABLED at iter 0.C; enabled in iter 9.3.
# Three tuners share a single weekly window; offset by minutes to avoid lock contention.
# ---------------------------------------------------------------------------
resource "aws_scheduler_schedule" "tuner_targeting_weekly" {
  name       = "tuner-targeting-weekly"
  group_name = aws_scheduler_schedule_group.pipeline.name
  state      = "DISABLED" # iter 9.3 enables

  schedule_expression          = "cron(0 2 ? * SUN *)"
  schedule_expression_timezone = "UTC"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = "arn:aws:lambda:${var.aws_region}:${var.aws_account_id}:function:ai-website-agency-tuner-targeting${local.env_suffix}"
    role_arn = aws_iam_role.scheduler_invoke.arn

    retry_policy {
      maximum_event_age_in_seconds = 3600
      maximum_retry_attempts       = 3
    }
  }
}

resource "aws_scheduler_schedule" "tuner_style_weekly" {
  name       = "tuner-style-weekly"
  group_name = aws_scheduler_schedule_group.pipeline.name
  state      = "DISABLED"

  schedule_expression          = "cron(5 2 ? * SUN *)"
  schedule_expression_timezone = "UTC"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = "arn:aws:lambda:${var.aws_region}:${var.aws_account_id}:function:ai-website-agency-tuner-style${local.env_suffix}"
    role_arn = aws_iam_role.scheduler_invoke.arn

    retry_policy {
      maximum_event_age_in_seconds = 3600
      maximum_retry_attempts       = 3
    }
  }
}

resource "aws_scheduler_schedule" "tuner_email_tone_weekly" {
  name       = "tuner-email-tone-weekly"
  group_name = aws_scheduler_schedule_group.pipeline.name
  state      = "DISABLED"

  schedule_expression          = "cron(10 2 ? * SUN *)"
  schedule_expression_timezone = "UTC"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = "arn:aws:lambda:${var.aws_region}:${var.aws_account_id}:function:ai-website-agency-tuner-email-tone${local.env_suffix}"
    role_arn = aws_iam_role.scheduler_invoke.arn

    retry_policy {
      maximum_event_age_in_seconds = 3600
      maximum_retry_attempts       = 3
    }
  }
}

# Pipeline Lambdas need permission to publish to the bus.
resource "aws_iam_role_policy" "events_publish" {
  name = "events-publish"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["events:PutEvents"]
      Resource = aws_cloudwatch_event_bus.pipeline.arn
    }]
  })
}
