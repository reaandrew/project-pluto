# Cognito user pool + Hosted UI for the admin app, and the JWT authorizer that
# the API uses to validate operator sessions. Per `.ralph/specs/08-admin-ui.md`
# § Auth.
#
# The BFF CloudFront distribution's viewer-request CFFn (in aws-setup/) translates
# the `auth_token` cookie into `Authorization: Bearer <jwt>` so this authorizer
# sees a standard bearer token regardless of whether the request came from the
# admin app (cookie) or a programmatic caller (raw bearer).

resource "aws_cognito_user_pool" "operators" {
  name = "ai-website-agency${local.env_suffix}-operators"

  username_attributes      = ["email"]
  auto_verified_attributes = ["email"]

  password_policy {
    minimum_length                   = 14
    require_lowercase                = true
    require_uppercase                = true
    require_numbers                  = true
    require_symbols                  = true
    temporary_password_validity_days = 3
  }

  mfa_configuration = "OPTIONAL"
  software_token_mfa_configuration {
    enabled = true
  }

  account_recovery_setting {
    recovery_mechanism {
      name     = "verified_email"
      priority = 1
    }
  }

  admin_create_user_config {
    allow_admin_create_user_only = true # operator pool — no self-signup
  }

  deletion_protection = local.is_production ? "ACTIVE" : "INACTIVE"

  schema {
    name                     = "email"
    attribute_data_type      = "String"
    required                 = true
    mutable                  = true
    developer_only_attribute = false
    string_attribute_constraints {
      min_length = 5
      max_length = 256
    }
  }

  tags = local.common_tags
}

# Operator group — used by the BFF authorizer to gate operator-only API routes
# (`PATCH /settings`, `POST /admin/discover/run`, etc.).
resource "aws_cognito_user_group" "operator" {
  user_pool_id = aws_cognito_user_pool.operators.id
  name         = "operator"
  description  = "Operators who can review queue items + tune the pipeline"
  precedence   = 10
}

# Hosted UI domain. Per-env so preview envs can authenticate independently.
resource "aws_cognito_user_pool_domain" "hosted_ui" {
  user_pool_id = aws_cognito_user_pool.operators.id
  domain       = "ai-website-agency${local.env_suffix}-auth"
}

resource "aws_cognito_user_pool_client" "admin_app" {
  name         = "ai-website-agency${local.env_suffix}-admin-app"
  user_pool_id = aws_cognito_user_pool.operators.id

  generate_secret = false # SPA — public client, PKCE only

  allowed_oauth_flows                  = ["code"]
  allowed_oauth_scopes                 = ["email", "openid", "profile"]
  allowed_oauth_flows_user_pool_client = true

  callback_urls = local.is_production ? [
    "https://${var.base_domain}/oauth/callback",
    ] : [
    "https://preview.${var.base_domain}/${local.env_sanitized}/oauth/callback",
  ]
  logout_urls = local.is_production ? [
    "https://${var.base_domain}/",
    ] : [
    "https://preview.${var.base_domain}/${local.env_sanitized}/",
  ]

  supported_identity_providers = ["COGNITO"]

  access_token_validity  = 60
  id_token_validity      = 60
  refresh_token_validity = 30
  token_validity_units {
    access_token  = "minutes"
    id_token      = "minutes"
    refresh_token = "days"
  }

  prevent_user_existence_errors = "ENABLED"

  explicit_auth_flows = [
    "ALLOW_USER_SRP_AUTH",
    "ALLOW_REFRESH_TOKEN_AUTH",
  ]
}

# JWT authorizer on the HTTP API. Future routes that need operator auth attach
# this via `authorizer_id` on aws_apigatewayv2_route. The /health route stays
# open (smoke-test surface).
resource "aws_apigatewayv2_authorizer" "cognito" {
  api_id           = aws_apigatewayv2_api.main.id
  authorizer_type  = "JWT"
  identity_sources = ["$request.header.Authorization"]
  name             = "ai-website-agency${local.env_suffix}-cognito"

  jwt_configuration {
    audience = [aws_cognito_user_pool_client.admin_app.id]
    issuer   = "https://cognito-idp.${var.aws_region}.amazonaws.com/${aws_cognito_user_pool.operators.id}"
  }
}

# Outputs consumed by the admin-app runtime-config (the deploy step writes them
# into runtime-config.js so the React app knows where to redirect for login).
output "cognito_user_pool_id" {
  value       = aws_cognito_user_pool.operators.id
  description = "Cognito user pool ID — for runtime-config.js"
}

output "cognito_app_client_id" {
  value       = aws_cognito_user_pool_client.admin_app.id
  description = "Cognito app client ID — for runtime-config.js"
}

output "cognito_hosted_ui_domain" {
  value       = aws_cognito_user_pool_domain.hosted_ui.domain
  description = "Cognito Hosted UI subdomain (full URL: https://<domain>.auth.<region>.amazoncognito.com/)"
}

output "cognito_authorizer_id" {
  value       = aws_apigatewayv2_authorizer.cognito.id
  description = "API Gateway JWT authorizer ID — referenced by routes that need operator auth"
}
