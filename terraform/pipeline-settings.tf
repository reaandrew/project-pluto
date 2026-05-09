# Iter 0.F.1 — seed the singleton PipelineSettings row in the items table.
#
# pkg/killswitch reads this row on every consumer Lambda's entry (cached 60s
# in-process); operators edit it via the /settings BFF page (iter 0.F.2 +
# 0.G.3). The keys + JSON shape MUST stay in lockstep with pkg/killswitch:
#
#   pk = SETTINGS#PIPELINE
#   sk = CURRENT
#
# `lifecycle.ignore_changes = [item]` — same pattern as
# `aws_ssm_parameter.example_app_secret` for SSM secrets (Pitfall #4):
# Terraform creates the row once with the documented defaults; from then on
# it is owned by the operator. Re-running terraform apply does not revert
# operator toggles. On per-PR env teardown the row goes away with the table;
# next per-PR env creation re-seeds with defaults — exactly what we want
# for ephemeral envs.
#
# Defaults mirror lambdas/pkg/killswitch.Defaults() AND
# .ralph/specs/05-capacity-and-cost.md § "The Pipeline Settings record".
# When the doc changes, change BOTH the Go constants and the JSON below
# and bump the affected lambdas' deploy snapshot — the test suite asserts
# Defaults() unmarshalling matches this seed shape.

resource "aws_dynamodb_table_item" "pipeline_settings" {
  table_name = aws_dynamodb_table.items.name
  hash_key   = aws_dynamodb_table.items.hash_key
  range_key  = aws_dynamodb_table.items.range_key

  item = <<-ITEM
  {
    "pk":              {"S": "SETTINGS#PIPELINE"},
    "sk":              {"S": "CURRENT"},
    "pipelineEnabled": {"BOOL": true},
    "stages": {
      "M": {
        "discoveryEnabled": {"BOOL": true},
        "auditEnabled":     {"BOOL": true},
        "previewEnabled":   {"BOOL": true},
        "outreachEnabled":  {"BOOL": false}
      }
    },
    "caps": {
      "M": {
        "maxDiscoveriesPerDay": {"N": "100"},
        "maxAuditsPerDay":      {"N": "50"},
        "maxPreviewsPerDay":    {"N": "10"},
        "maxEmailsPerDay":      {"N": "5"},
        "maxReviewQueueSize":   {"N": "20"},
        "maxBacklogSize":       {"N": "500"}
      }
    },
    "thresholds": {
      "M": {
        "minTechnicalIssueScore": {"N": "30"},
        "minQualificationScore":  {"N": "70"},
        "minContactConfidence":   {"N": "0.6"}
      }
    },
    "budgets": {
      "M": {
        "dailyBedrockUsd": {"N": "5"},
        "dailyPlacesUsd":  {"N": "2"},
        "dailyEmailUsd":   {"N": "1"}
      }
    }
  }
  ITEM

  lifecycle {
    ignore_changes = [item]
  }
}
