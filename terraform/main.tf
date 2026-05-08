terraform {
  required_version = ">= 1.9.0"

  required_providers {
    aws     = { source = "hashicorp/aws", version = "~> 5.0" }
    archive = { source = "hashicorp/archive", version = "~> 2.0" }
  }

  # Backend `key` is INTENTIONALLY OMITTED — CI passes it via -backend-config so
  # a typo can never write a branch's state into prod. See docs/BOOTSTRAP.md
  # and .github/workflows/deploy.yml.
  #
  #   terraform init \
  #     -backend-config="bucket=ai-website-agency-terraform-state-276447169330" \
  #     -backend-config="key=terraform/ai-website-agency/${ENVIRONMENT}/terraform.tfstate" \
  #     -backend-config="region=eu-west-2"
  backend "s3" {
    use_lockfile = true
    encrypt      = true
  }
}

provider "aws" {
  region = var.aws_region
  default_tags {
    tags = {
      Project     = "ai-website-agency"
      Environment = var.environment
      ManagedBy   = "terraform"
      Stack       = "per-env"
    }
  }
}

variable "aws_region" {
  type        = string
  default     = "eu-west-2"
  description = "Primary AWS region"
}

variable "environment" {
  type        = string
  default     = "development"
  description = "`production` for main branch, sanitised branch name otherwise. CI sets this from scripts/derive-env-name.sh."
}

variable "base_domain" {
  type        = string
  default     = "agency.techar.ch"
  description = "Project domain — read into local.api_domain etc."
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}
