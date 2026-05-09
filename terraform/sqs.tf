# Dead-letter queues per planned consumer, defined here in iter 0.C so the queues
# exist when each Lambda lands in subsequent iterations. The actual consumer
# queues + EventBridge SQS targets are added per consumer (iter 1.3 → 8.5+).
#
# Convention: each Lambda has a *main* queue (created with the Lambda) feeding
# the function via event-source mapping, and a *DLQ* (created here) for messages
# that fail the retry budget. 14-day retention per `.ralph/specs/03-events.md`
# (also AWS SQS hard maximum).

locals {
  pipeline_consumers = [
    "discover",         # iter 1.3
    "audit",            # iter 2.3
    "qualifier",        # iter 3.2
    "backlog-promoter", # iter 3.3
    "spec-generator",   # iter 4.2
    "generator",        # iter 5.2
    "publisher",        # iter 5.3
    "email-draft",      # iter 7.2
    "sender",           # iter 8.2
    "reply-triage",     # iter 8.5
    "ses-feedback",     # iter 8.3 — SES bounce/complaint subscriber
    "tuner-targeting",  # iter 9.3
    "tuner-style",      # iter 9.3
    "tuner-email-tone", # iter 9.3
  ]
}

resource "aws_sqs_queue" "dlq" {
  for_each = toset(local.pipeline_consumers)

  name                       = "ai-website-agency-${each.key}-dlq${local.env_suffix}"
  message_retention_seconds  = 1209600 # 14 days (AWS max)
  visibility_timeout_seconds = 60
  sqs_managed_sse_enabled    = true

  tags = merge(local.common_tags, {
    Component = "${each.key}-dlq"
  })
}

# Lambdas may need to manually re-drive from DLQs (operator action via the admin app
# in a future iteration). Grant the shared role read/delete on every DLQ.
resource "aws_iam_role_policy" "sqs_dlq_redrive" {
  name = "sqs-dlq-redrive"
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
        "sqs:SendMessage", # for re-drive into the consumer's main queue
      ]
      Resource = [for q in aws_sqs_queue.dlq : q.arn]
    }]
  })
}

output "dlq_arns" {
  value       = { for k, q in aws_sqs_queue.dlq : k => q.arn }
  description = "Map of consumer name → DLQ ARN. Each consumer wires this as its `redrive_policy.deadLetterTargetArn` when its main queue is added."
}
