# ONE Lambda execution role + policies grouped BY PERMISSION LEVEL (not per-table).
# Pitfall #6: long branch names + per-table policies push the inline-policy size
# over the 10KB limit (smm 34d7d25). Adding new tables = append actions to existing
# groups; never create per-table policy resources.

resource "aws_iam_role" "lambda_api" {
  name        = "ai-website-agency-lambda-api${local.env_suffix}"
  description = "Execution role for ai-website-agency-api-* Lambdas (env: ${var.environment})"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Service = "lambda.amazonaws.com"
      }
      Action = "sts:AssumeRole"
    }]
  })

  tags = local.common_tags
}

resource "aws_iam_role_policy" "cloudwatch_logs" {
  name = "cloudwatch-logs"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "logs:CreateLogGroup",
        "logs:CreateLogStream",
        "logs:PutLogEvents",
      ]
      Resource = "arn:aws:logs:*:*:*"
    }]
  })
}

resource "aws_iam_role_policy" "dynamodb_read" {
  name = "dynamodb-read"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "dynamodb:GetItem",
        "dynamodb:BatchGetItem",
        "dynamodb:Query",
        "dynamodb:Scan",
        "dynamodb:DescribeTable",
      ]
      # ARN wildcards on indexes — never enumerate per-index (smm 34d7d25).
      Resource = [
        aws_dynamodb_table.items.arn,
        "${aws_dynamodb_table.items.arn}/index/*",
      ]
    }]
  })
}

resource "aws_iam_role_policy" "dynamodb_write" {
  name = "dynamodb-write"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "dynamodb:PutItem",
        "dynamodb:UpdateItem",
        "dynamodb:DeleteItem",
        "dynamodb:BatchWriteItem",
      ]
      Resource = aws_dynamodb_table.items.arn
    }]
  })
}

resource "aws_iam_role_policy" "ssm_read" {
  name = "ssm-read"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "ssm:GetParameter",
        "ssm:GetParameters",
        "ssm:GetParametersByPath",
      ]
      # Per-env namespace only.
      Resource = "arn:aws:ssm:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:parameter/ai-website-agency/${var.environment}/*"
    }]
  })
}

resource "aws_iam_role_policy" "xray" {
  name = "xray"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "xray:PutTraceSegments",
        "xray:PutTelemetryRecords",
      ]
      Resource = "*"
    }]
  })
}
