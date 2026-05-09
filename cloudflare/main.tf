# Cloudflare stack — R2 + Workers KV + Workers Rate Limiting + Worker route.
# Per `.ralph/specs/01-architecture.md` § "Generated business previews".
#
# This is a separate Terraform state from terraform/ (per-env AWS) and
# aws-setup/ (singleton AWS), keyed at
#   s3://ai-website-agency-terraform-state-<acct>/terraform/cloudflare/<env>/terraform.tfstate
# so a botched Cloudflare apply never touches AWS state.

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

  backend "s3" {
    bucket       = "ai-website-agency-terraform-state-276447169330"
    key          = "terraform/cloudflare/terraform.tfstate"
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
  # Required scopes: Workers Scripts:Edit, Workers KV Storage:Edit, R2:Edit, Zone DNS:Edit.
}

variable "environment" {
  type        = string
  description = "Per-env name (production | <branch>). Single source: scripts/derive-env-name.sh."
}

variable "cloudflare_account_id" {
  type        = string
  description = "Cloudflare account ID (from dash.cloudflare.com top-right)."
}

variable "cloudflare_zone_id" {
  type        = string
  description = "Zone ID for the parent domain (techar.ch — fetched via the cloudflare provider's data source if not provided)."
  default     = ""
}

variable "preview_subdomain" {
  type        = string
  default     = "previews.agency.techar.ch"
  description = "Hostname under which the Worker serves passcode-gated previews. Per-env routing happens via Worker code, not DNS."
}

locals {
  is_production = var.environment == "production"
  env_sanitized = substr(replace(lower(var.environment), "/[^a-z0-9-]/", "-"), 0, 24)
  env_suffix    = local.is_production ? "" : "-${local.env_sanitized}"

  common_tags = {
    project     = "ai-website-agency"
    environment = var.environment
    managed_by  = "terraform"
  }
}

# R2 bucket — generated preview HTML, screenshots, asset bundles. One bucket per env.
# Lifecycle rule: 90 days for objects without `keep=true` metadata.
resource "cloudflare_r2_bucket" "previews" {
  account_id = var.cloudflare_account_id
  name       = "ai-website-agency-previews${local.env_suffix}"
  location   = "WEUR" # Western Europe
}

resource "cloudflare_r2_bucket_lifecycle" "previews" {
  account_id  = var.cloudflare_account_id
  bucket_name = cloudflare_r2_bucket.previews.name

  rule {
    id      = "expire-without-keep-tag"
    enabled = true

    conditions {
      prefix = "" # apply to all objects
    }

    delete_objects_transition {
      condition {
        type           = "Age"
        max_age        = 7776000 # 90 days in seconds
        date           = null
        days_after_age = null
      }
    }

    abort_multipart_uploads_transition {
      condition {
        type    = "Age"
        max_age = 604800 # 7 days
      }
    }
  }
}

# Workers KV — passcode hashes per websiteId. Key = "passcode:<websiteId>",
# value = argon2id hash. The publisher (iter 5.3) writes; the Worker reads.
resource "cloudflare_workers_kv_namespace" "preview_passcodes" {
  account_id = var.cloudflare_account_id
  title      = "PREVIEW_PASSCODES${local.env_suffix}"
}

# Workers Rate Limiting — built-in binding. Limits brute-force on the passcode
# form. 10 req / 60s per IP. Per-Worker namespace.
resource "cloudflare_ruleset" "preview_rate_limit" {
  account_id  = var.cloudflare_account_id
  name        = "ai-website-agency-preview-ratelimit${local.env_suffix}"
  description = "Brute-force protection on POST /sites/<websiteId> passcode form"
  kind        = "zone"
  phase       = "http_ratelimit"
  zone_id     = var.cloudflare_zone_id

  rules {
    action      = "block"
    description = "10 POSTs / 60s / IP on the passcode form"
    expression  = "(http.request.method eq \"POST\" and starts_with(http.request.uri.path, \"/sites/\"))"
    enabled     = true

    ratelimit {
      characteristics     = ["cf.colo.id", "ip.src"]
      period              = 60
      requests_per_period = 10
      mitigation_timeout  = 600 # 10-minute lockout after limit hit
    }
  }
}

# Worker script + route. The wrangler-deployed worker (deployed by
# .github/workflows/cloudflare.yml) is named `ai-website-agency-preview${env_suffix}`.
# Terraform claims the route binding so the host-pattern is in code.
resource "cloudflare_worker_route" "preview" {
  zone_id     = var.cloudflare_zone_id
  pattern     = local.is_production ? "${var.preview_subdomain}/*" : "${local.env_sanitized}.${var.preview_subdomain}/*"
  script_name = "ai-website-agency-preview${local.env_suffix}"
}

output "r2_bucket_name" {
  value = cloudflare_r2_bucket.previews.name
}

output "kv_namespace_id" {
  value       = cloudflare_workers_kv_namespace.preview_passcodes.id
  description = "Wrangler binding ID for the worker's KV namespace"
}

output "worker_route_pattern" {
  value = cloudflare_worker_route.preview.pattern
}
