resource "aws_dynamodb_table" "items" {
  name         = "website-agency-items${local.env_suffix}"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }

  # Pitfall #2 (smm 1f225d5): production-only — preview envs need to destroy.
  point_in_time_recovery {
    enabled = local.is_production
  }
  deletion_protection_enabled = local.is_production

  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }

  tags = local.common_tags
}
