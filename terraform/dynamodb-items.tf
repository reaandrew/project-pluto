resource "aws_dynamodb_table" "items" {
  name         = "ai-website-agency-items${local.env_suffix}"
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

  # Per `.ralph/specs/02-data-model.md`:
  #   - gsi1 — status-based access (queue listings, backlog ordering by priorityScore).
  #   - gsi2 — vertical-based access (analytics, tuner aggregations).
  #   - gsi3 — domain-uniqueness lookup (one item per domain across the project).
  attribute {
    name = "gsi1pk"
    type = "S"
  }
  attribute {
    name = "gsi1sk"
    type = "S"
  }
  attribute {
    name = "gsi2pk"
    type = "S"
  }
  attribute {
    name = "gsi2sk"
    type = "S"
  }
  attribute {
    name = "gsi3pk"
    type = "S"
  }
  attribute {
    name = "gsi3sk"
    type = "S"
  }

  global_secondary_index {
    name            = "gsi1"
    hash_key        = "gsi1pk"
    range_key       = "gsi1sk"
    projection_type = "ALL" # queue + backlog reads need full item
  }
  global_secondary_index {
    name            = "gsi2"
    hash_key        = "gsi2pk"
    range_key       = "gsi2sk"
    projection_type = "ALL" # tuner aggregations consume full item
  }
  global_secondary_index {
    name            = "gsi3"
    hash_key        = "gsi3pk"
    range_key       = "gsi3sk"
    projection_type = "KEYS_ONLY" # domain-uniqueness check only needs presence
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
