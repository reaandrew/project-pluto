# ONE grouped Bedrock-invoke policy on the shared Lambda execution role.
# Skeleton pitfall #6: per-model policies bloat the inline-policy size and
# multiply when we add more agency-specific Lambdas. Add new models to this
# Resource list — never create per-model policy resources.
#
# Models per `.ralph/specs/07-bedrock-prompts.md`:
#   - anthropic.claude-haiku-4-5  — triage / audit / qualifier / email-draft / targeting+tone tuners
#   - anthropic.claude-sonnet-4-6 — spec generation / site-copy refinement / style-guide tuner
#
# Region eu-west-2 (per `.ralph/specs/01-architecture.md`).

resource "aws_iam_role_policy" "bedrock_invoke" {
  name = "bedrock-invoke"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "bedrock:InvokeModel",
        "bedrock:InvokeModelWithResponseStream",
      ]
      Resource = [
        "arn:aws:bedrock:${var.aws_region}::foundation-model/anthropic.claude-haiku-4-5",
        "arn:aws:bedrock:${var.aws_region}::foundation-model/anthropic.claude-sonnet-4-6",
      ]
    }]
  })
}
