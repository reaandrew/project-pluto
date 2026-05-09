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

  # Per skeleton pitfall #9: backend key is intentionally NOT set here. CI supplies
  # `-backend-config="key=terraform/cloudflare/<env>/terraform.tfstate"` so each env
  # gets its own state file. A bare `terraform init` (no -backend-config) will
  # prompt for the key, which is the right safety property.
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

variable "preview_zone_name" {
  type        = string
  default     = "previews.agency.techar.ch"
  description = "Cloudflare zone name. The Worker serves traffic for this zone + its subdomains. Must be added to your Cloudflare account once (dash → Add Site → Free plan)."
}

# Cloudflare zone — created via API. Requires the token to have
# Account-level zone-management permission (Account: Account Settings: Edit,
# or Zone: Zone: Edit on All zones). If the token only has Zone DNS:Edit on
# specific zones, this CreateZone call returns 403 and the apply fails with
# a clear permission-denied error.
#
# Production-env applies own this resource; preview-env applies just READ
# the same zone (terraform's `cloudflare_zone` resource is per-account-zone,
# not per-env, so we use a `count` guard to avoid every preview env trying
# to recreate the same zone).
resource "cloudflare_zone" "preview" {
  count = local.is_production ? 1 : 0

  account_id = var.cloudflare_account_id
  zone       = var.preview_zone_name
  plan       = "free"
  type       = "full"
}

# Preview envs reference the production-created zone via data source.
data "cloudflare_zone" "preview" {
  name = var.preview_zone_name

  # On the very first production apply, the zone resource above creates the zone
  # so the data source resolves only after that completes.
  depends_on = [cloudflare_zone.preview]
}

# NS delegation record in the AWS-managed agency.techar.ch zone, pointing
# previews.agency.techar.ch at Cloudflare's nameservers. Production-only — the
# delegation is a singleton record regardless of how many preview envs run.
data "aws_ssm_parameter" "agency_zone_id" {
  name = "/ai-website-agency/route53/zone_id"
}

resource "aws_route53_record" "cloudflare_delegation" {
  count = local.is_production ? 1 : 0

  zone_id = data.aws_ssm_parameter.agency_zone_id.value
  name    = var.preview_zone_name
  type    = "NS"
  ttl     = 300
  records = cloudflare_zone.preview[0].name_servers
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

# R2 lifecycle (90-day expiry on objects without `keep=true` metadata) is
# managed via the Cloudflare API, NOT via Terraform — there's no first-class
# `cloudflare_r2_bucket_lifecycle` resource in provider v4. Tracked as a
# follow-up: a small one-shot `wrangler r2 lifecycle put` call from
# .github/workflows/cloudflare.yml after `terraform apply`, or a Worker-side
# scheduled cleanup job. Either way, not blocking iter 0.D substrate.

# Workers KV — passcode hashes per websiteId. Key = "passcode:<websiteId>",
# value = argon2id hash. The publisher (iter 5.3) writes; the Worker reads.
resource "cloudflare_workers_kv_namespace" "preview_passcodes" {
  account_id = var.cloudflare_account_id
  title      = "PREVIEW_PASSCODES${local.env_suffix}"
}

# Zone-level rate limit ruleset. Limits brute-force on the passcode form.
# 10 POSTs / 60s / IP, 10-minute mitigation lockout. Zone-level rulesets MUST
# carry zone_id and MUST NOT carry account_id (the provider rejects both).
resource "cloudflare_ruleset" "preview_rate_limit" {
  zone_id     = data.cloudflare_zone.preview.id
  name        = "ai-website-agency-preview-ratelimit${local.env_suffix}"
  description = "Brute-force protection on POST /sites/<websiteId> passcode form"
  kind        = "zone"
  phase       = "http_ratelimit"

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
resource "cloudflare_workers_route" "preview" {
  zone_id     = data.cloudflare_zone.preview.id
  pattern     = local.is_production ? "${var.preview_zone_name}/*" : "${local.env_sanitized}.${var.preview_zone_name}/*"
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
  value = cloudflare_workers_route.preview.pattern
}

output "delegation_nameservers" {
  value       = data.cloudflare_zone.preview.name_servers
  description = "Cloudflare-assigned nameservers for the preview zone. Production env's terraform apply writes these as NS records in the agency.techar.ch Route53 zone."
}
