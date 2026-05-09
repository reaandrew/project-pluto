# Cloudflare stack — R2 + Workers KV + Worker (no custom hostname yet).
# Per `.ralph/specs/01-architecture.md` § "Generated business previews".
#
# This is a separate Terraform state from terraform/ (per-env AWS) and
# aws-setup/ (singleton AWS), keyed at
#   s3://ai-website-agency-terraform-state-<acct>/terraform/cloudflare/<env>/terraform.tfstate
# so a botched Cloudflare apply never touches AWS state.
#
# CUSTOM HOSTNAME (deferred to iter 5.3):
#   Cloudflare's "Add a Site" UI only accepts apex / registered domains
#   (techar.ch), not subdomains (previews.agency.techar.ch). To get a custom
#   hostname for the Worker we need to either (a) add a full domain to
#   Cloudflare DNS — moving DNS off Route53 — or (b) buy/repurpose another
#   domain to host on Cloudflare. Both are out of scope for iter 0.D, whose
#   goal is the Worker substrate. The Worker is reachable at
#     ai-website-agency-preview-<env>.<your-cf-subdomain>.workers.dev
#   which is enough for iter 5.x to build against.

terraform {
  required_version = "~> 1.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4.0"
    }
  }

  # Per skeleton pitfall #9: backend key is intentionally NOT set here. CI supplies
  # `-backend-config="key=terraform/cloudflare/<env>/terraform.tfstate"` so each env
  # gets its own state file.
  backend "s3" {
    bucket       = "ai-website-agency-terraform-state-276447169330"
    region       = "eu-west-2"
    use_lockfile = true
    encrypt      = true
  }
}

provider "aws" {
  region = "eu-west-2"
}

provider "cloudflare" {
  # API token from CLOUDFLARE_API_TOKEN env var (set by CI from GH secret).
  # Required scopes: Workers Scripts:Edit, Workers KV Storage:Edit, R2:Edit.
}

variable "environment" {
  type        = string
  description = "Per-env name (production | <branch>). Single source: scripts/derive-env-name.sh."
}

variable "cloudflare_account_id" {
  type        = string
  description = "Cloudflare account ID (from dash.cloudflare.com top-right)."
}

locals {
  is_production = var.environment == "production"
  env_sanitized = substr(replace(lower(var.environment), "/[^a-z0-9-]/", "-"), 0, 24)
  env_suffix    = local.is_production ? "" : "-${local.env_sanitized}"
}

# R2 bucket — generated preview HTML, screenshots, asset bundles. One bucket per env.
# Lifecycle rule (90d expiry on `keep!=true`) is managed via Cloudflare API directly,
# not Terraform — provider v4 has no `cloudflare_r2_bucket_lifecycle` resource. Set
# via `wrangler r2 lifecycle put` from cloudflare.yml after apply (TODO iter 5.3).
resource "cloudflare_r2_bucket" "previews" {
  account_id = var.cloudflare_account_id
  name       = "ai-website-agency-previews${local.env_suffix}"
  location   = "WEUR" # Western Europe
}

# Workers KV — passcode hashes per websiteId. Key = "passcode:<websiteId>",
# value = argon2id hash. The publisher (iter 5.3) writes; the Worker reads.
resource "cloudflare_workers_kv_namespace" "preview_passcodes" {
  account_id = var.cloudflare_account_id
  title      = "PREVIEW_PASSCODES${local.env_suffix}"
}

output "r2_bucket_name" {
  value = cloudflare_r2_bucket.previews.name
}

output "kv_namespace_id" {
  value       = cloudflare_workers_kv_namespace.preview_passcodes.id
  description = "Wrangler binding ID for the worker's KV namespace"
}
