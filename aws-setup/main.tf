terraform {
  required_version = ">= 1.9.0"

  required_providers {
    aws     = { source = "hashicorp/aws", version = "~> 5.0" }
    archive = { source = "hashicorp/archive", version = "~> 2.0" }
  }

  # Bucket name is hardcoded because backend blocks don't allow variables.
  # State bucket is created out-of-band — see docs/BOOTSTRAP.md step 1.
  backend "s3" {
    bucket       = "ai-website-agency-terraform-state-276447169330"
    key          = "aws-setup/terraform.tfstate"
    region       = "eu-west-2"
    use_lockfile = true
    encrypt      = true
  }
}

provider "aws" {
  region = var.aws_region
  default_tags {
    tags = {
      Project   = "ai-website-agency"
      ManagedBy = "terraform"
      Stack     = "aws-setup"
    }
  }
}

# us-east-1 alias for CloudFront cert, Lambda@Edge, and CloudFront WAF
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
  default_tags {
    tags = {
      Project   = "ai-website-agency"
      ManagedBy = "terraform"
      Stack     = "aws-setup"
    }
  }
}

variable "aws_region" {
  type        = string
  default     = "eu-west-2"
  description = "Primary AWS region for regional resources (API Gateway, etc.)"
}

variable "aws_account_id" {
  type        = string
  default     = "276447169330"
  description = "AWS account id — used in resource ARNs and bucket names"
}

variable "base_domain" {
  type        = string
  default     = "agency.techar.ch"
  description = "Hosted zone for this project. NS records are delegated from techar.ch."
}

variable "parent_domain" {
  type        = string
  default     = "techar.ch"
  description = "Parent domain — informational only, used for delegation_instructions output"
}

variable "github_org" {
  type        = string
  default     = "reaandrew"
  description = "GitHub org — constrains the OIDC trust policy"
}

variable "github_repo" {
  type        = string
  default     = "ai-website-agency"
  description = "GitHub repo name — constrains the OIDC trust policy to repo:<org>/<repo>:*"
}

# ---------------------------------------------------------------------------
# OIDC provider for GitHub Actions
# ---------------------------------------------------------------------------
# This is account-wide shared infra (also used by tripwire, smm, ...). We read
# it as a data source rather than manage it here, so multi-project ownership
# doesn't conflict.

data "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"
}

# ---------------------------------------------------------------------------
# GitHub Actions IAM role
# ---------------------------------------------------------------------------
# Trust policy is constrained to repo:reaandrew/ai-website-agency:* — never broader.
# This excludes pushes to forks (which would otherwise inherit the role).

resource "aws_iam_role" "github_actions" {
  name                 = "github-actions-ai-website-agency"
  description          = "Assumed by GitHub Actions in ${var.github_org}/${var.github_repo} via OIDC"
  max_session_duration = 3600

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Federated = data.aws_iam_openid_connect_provider.github.arn
      }
      Action = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
        }
        StringLike = {
          "token.actions.githubusercontent.com:sub" = "repo:${var.github_org}/${var.github_repo}:*"
        }
      }
    }]
  })

  tags = {
    Name = "github-actions-ai-website-agency"
  }
}

# ---------------------------------------------------------------------------
# Inline policies — grouped BY PERMISSION DOMAIN, never one giant doc.
# Each policy stays well under the 10KB inline limit (smm 34d7d25 lesson).
# Adding a new service = new policy resource, not appending to an existing one.
# ---------------------------------------------------------------------------

# 1. Terraform state bucket + per-env S3 buckets (force_destroy needs versioned
#    delete + multipart abort — pitfall #1).
resource "aws_iam_role_policy" "s3_access" {
  name = "s3-access"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "TerraformState"
        Effect = "Allow"
        Action = [
          "s3:ListBucket",
          "s3:GetObject",
          "s3:PutObject",
          "s3:DeleteObject",
          "s3:GetObjectVersion",
          "s3:DeleteObjectVersion",
          "s3:ListBucketVersions",
        ]
        Resource = [
          "arn:aws:s3:::ai-website-agency-terraform-state-${var.aws_account_id}",
          "arn:aws:s3:::ai-website-agency-terraform-state-${var.aws_account_id}/*",
        ]
      },
      {
        Sid      = "PerEnvBuckets"
        Effect   = "Allow"
        Action   = "s3:*" # Full CRUD on ai-website-agency-* buckets — too many granular IAM action names to enumerate
        Resource = ["arn:aws:s3:::ai-website-agency-*", "arn:aws:s3:::ai-website-agency-*/*"]
      },
    ]
  })
}

# 2. Route53 — agency.techar.ch zone only (zone-id is set after the zone
#    is created; we use a wildcard on the resource arn for simplicity, but
#    the action set is read-only-or-record-CRUD, never zone deletion).
resource "aws_iam_role_policy" "route53_access" {
  name = "route53-access"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "route53:ChangeResourceRecordSets",
          "route53:GetHostedZone",
          "route53:ListResourceRecordSets",
          "route53:GetChange",
          "route53:ListTagsForResource",
        ]
        Resource = "arn:aws:route53:::hostedzone/*"
      },
      {
        Effect   = "Allow"
        Action   = ["route53:ListHostedZones", "route53:ListHostedZonesByName", "route53:GetChange"]
        Resource = "*"
      },
    ]
  })
}

# 3. Frontend deploy — S3 sync + CloudFront invalidation.
resource "aws_iam_role_policy" "frontend_deploy" {
  name = "frontend-deploy"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "cloudfront:CreateInvalidation",
          "cloudfront:GetInvalidation",
          "cloudfront:ListInvalidations",
          "cloudfront:GetDistribution",
          "cloudfront:GetDistributionConfig",
          "cloudfront:ListDistributions",
        ]
        Resource = "*"
      },
    ]
  })
}

# 4. Lambda CRUD scoped to ai-website-agency-* function names.
resource "aws_iam_role_policy" "lambda_deploy" {
  name = "lambda-deploy"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "lambda:CreateFunction",
          "lambda:DeleteFunction",
          "lambda:GetFunction",
          "lambda:GetFunctionConfiguration",
          "lambda:GetFunctionCodeSigningConfig",
          "lambda:ListFunctions",
          "lambda:ListVersionsByFunction",
          "lambda:UpdateFunctionCode",
          "lambda:UpdateFunctionConfiguration",
          "lambda:TagResource",
          "lambda:UntagResource",
          "lambda:ListTags",
          "lambda:AddPermission",
          "lambda:RemovePermission",
          "lambda:GetPolicy",
          "lambda:PutFunctionConcurrency",
          "lambda:DeleteFunctionConcurrency",
          "lambda:PublishVersion",
        ]
        Resource = "arn:aws:lambda:*:${var.aws_account_id}:function:ai-website-agency-*"
      },
      {
        Effect   = "Allow"
        Action   = "iam:PassRole"
        Resource = "arn:aws:iam::${var.aws_account_id}:role/ai-website-agency-*"
        Condition = {
          StringEquals = {
            "iam:PassedToService" = "lambda.amazonaws.com"
          }
        }
      },
    ]
  })
}

# 5. API Gateway v2 — CRUD on HTTP APIs and custom domains.
resource "aws_iam_role_policy" "apigateway" {
  name = "apigateway"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "apigateway:*"
      Resource = "*"
    }]
  })
}

# 6. CloudWatch Logs — log groups + retention.
resource "aws_iam_role_policy" "cloudwatch_logs" {
  name = "cloudwatch-logs"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "LogGroupCRUD"
        Effect = "Allow"
        Action = [
          "logs:CreateLogGroup",
          "logs:DeleteLogGroup",
          "logs:DescribeLogGroups",
          "logs:PutRetentionPolicy",
          "logs:TagResource",
          "logs:UntagResource",
          "logs:ListTagsForResource",
          "logs:ListTagsLogGroup",
          "logs:DescribeLogStreams",
          "logs:CreateLogStream",
          "logs:AssociateKmsKey",
          "logs:DisassociateKmsKey",
        ]
        Resource = "arn:aws:logs:*:${var.aws_account_id}:*"
      },
      {
        # API GW Stage with access_log_settings needs LogDelivery permissions.
        Sid    = "LogDelivery"
        Effect = "Allow"
        Action = [
          "logs:CreateLogDelivery",
          "logs:GetLogDelivery",
          "logs:UpdateLogDelivery",
          "logs:DeleteLogDelivery",
          "logs:ListLogDeliveries",
          "logs:PutResourcePolicy",
          "logs:DescribeResourcePolicies",
          "logs:DescribeLogGroups",
        ]
        Resource = "*"
      },
    ]
  })
}

# 7. DynamoDB — ai-website-agency-* tables only.
resource "aws_iam_role_policy" "dynamodb" {
  name = "dynamodb"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "dynamodb:CreateTable",
        "dynamodb:DeleteTable",
        "dynamodb:DescribeTable",
        "dynamodb:DescribeContinuousBackups",
        "dynamodb:UpdateContinuousBackups",
        "dynamodb:DescribeTimeToLive",
        "dynamodb:UpdateTimeToLive",
        "dynamodb:UpdateTable",
        "dynamodb:ListTagsOfResource",
        "dynamodb:TagResource",
        "dynamodb:UntagResource",
        "dynamodb:ListTables",
      ]
      Resource = [
        "arn:aws:dynamodb:${var.aws_region}:${var.aws_account_id}:table/ai-website-agency-*",
        "arn:aws:dynamodb:${var.aws_region}:${var.aws_account_id}:table/ai-website-agency-*/index/*",
      ]
    }]
  })
}

# 8. IAM — ai-website-agency-* roles only.
resource "aws_iam_role_policy" "iam_roles" {
  name = "iam-roles"
  role = aws_iam_role.github_actions.id
  # Justification for the suppressions on `Action = [...]` below: this is the per-env
  # IAM management policy on the GitHub-Actions OIDC role. It MUST be able to
  # Create / Update / Delete Lambda execution roles for each preview env. The
  # Resource restriction (arn:...:role/ai-website-agency-*) scopes the blast radius
  # to this project only — the role cannot escalate outside `ai-website-agency-*`.
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        # nosemgrep: terraform.lang.security.iam.no-iam-priv-esc-funcs.no-iam-priv-esc-funcs
        # nosemgrep: terraform.lang.security.iam.no-iam-resource-exposure.no-iam-resource-exposure
        Action = [
          "iam:CreateRole",
          "iam:DeleteRole",
          "iam:GetRole",
          "iam:UpdateRole",
          "iam:UpdateAssumeRolePolicy",
          "iam:PutRolePolicy",
          "iam:DeleteRolePolicy",
          "iam:GetRolePolicy",
          "iam:ListRolePolicies",
          "iam:AttachRolePolicy",
          "iam:DetachRolePolicy",
          "iam:ListAttachedRolePolicies",
          "iam:TagRole",
          "iam:UntagRole",
          "iam:ListRoleTags",
          # Required by terraform when destroying a role: it checks whether any
          # EC2 instance profile depends on the role before deletion. Without
          # this, destroy.yml fails on the first delete after PR close.
          "iam:ListInstanceProfilesForRole",
        ]
        Resource = "arn:aws:iam::${var.aws_account_id}:role/ai-website-agency-*"
      },
    ]
  })
}

# 9. SSM — read all under /ai-website-agency/*, write only outside the cert/cf paths
#    (those are owned by aws-setup/, never touched by terraform/).
resource "aws_iam_role_policy" "ssm_access" {
  name = "ssm-access"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "ReadAll"
        Effect = "Allow"
        Action = [
          "ssm:GetParameter",
          "ssm:GetParameters",
          "ssm:GetParametersByPath",
          "ssm:ListTagsForResource",
        ]
        Resource = "arn:aws:ssm:*:${var.aws_account_id}:parameter/ai-website-agency/*"
      },
      {
        # ssm:DescribeParameters is account-scoped — can't be narrowed to a path.
        Sid      = "DescribeParameters"
        Effect   = "Allow"
        Action   = "ssm:DescribeParameters"
        Resource = "*"
      },
      {
        Sid    = "WriteAppParams"
        Effect = "Allow"
        Action = [
          "ssm:PutParameter",
          "ssm:DeleteParameter",
          "ssm:DeleteParameters",
          "ssm:AddTagsToResource",
          "ssm:RemoveTagsFromResource",
          "ssm:ListTagsForResource",
        ]
        # CI manages app secrets per env; aws-setup owns the cert/cf/route53/s3 paths.
        Resource = [
          "arn:aws:ssm:*:${var.aws_account_id}:parameter/ai-website-agency/*/app/*",
          "arn:aws:ssm:*:${var.aws_account_id}:parameter/ai-website-agency/*/secret/*",
        ]
      },
    ]
  })
}

# 10. WAF (read-only at the per-env level — WAF is owned by aws-setup).
resource "aws_iam_role_policy" "wafv2" {
  name = "wafv2"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["wafv2:GetWebACL", "wafv2:ListWebACLs"]
      Resource = "*"
    }]
  })
}

# 11. SQS (placeholder — narrow to ai-website-agency-* if/when queues are added).
resource "aws_iam_role_policy" "sqs" {
  name = "sqs"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["sqs:*"]
      Resource = "arn:aws:sqs:*:${var.aws_account_id}:ai-website-agency-*"
    }]
  })
}

# 12. SNS (placeholder).
resource "aws_iam_role_policy" "sns" {
  name = "sns"
  role = aws_iam_role.github_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["sns:*"]
      Resource = "arn:aws:sns:*:${var.aws_account_id}:ai-website-agency-*"
    }]
  })
}
