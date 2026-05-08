output "environment" {
  value = var.environment
}

output "api_endpoint" {
  description = "API Gateway raw endpoint (the *.execute-api... URL). Smoke tests use api_custom_domain."
  value       = aws_apigatewayv2_api.main.api_endpoint
}

output "api_custom_domain" {
  description = "Custom domain for the API: https://api[.|-<env>].agency.andrewreaassociates.com"
  value       = "https://${local.api_domain}"
}

output "bff_url" {
  description = "BFF URL — production: bff.agency.andrewreaassociates.com, preview: <env>.bff.agency.andrewreaassociates.com"
  value       = "https://${local.bff_domain}"
}

output "frontend_url" {
  description = "Frontend URL — production: agency.andrewreaassociates.com, preview: preview.agency.andrewreaassociates.com/<env>/"
  value       = local.frontend_url
}

output "s3_upload_path" {
  description = "Per-env S3 path for `aws s3 sync frontend/dist/ <this>/`."
  value       = local.s3_upload_path
  sensitive   = true # SSM data sources mark their .value as sensitive
}

output "cloudfront_id" {
  description = "CloudFront distribution id to invalidate after frontend deploy."
  value       = local.cloudfront_id
  sensitive   = true # SSM data sources mark their .value as sensitive
}

output "items_table_name" {
  value = aws_dynamodb_table.items.name
}

output "uploads_bucket" {
  value = aws_s3_bucket.uploads.id
}

output "lambda_function_name" {
  value = aws_lambda_function.api_hello.function_name
}
