# Iter 4.1 — seed VerticalStyleGuide rows in the items table.
#
# Two seed rows: "default" (the fallback the spec-generator reads when no
# vertical-specific guide exists) and "accountants" (the project's first
# concrete vertical — used in test fixtures + the smoke-test BFF flow).
#
# Operators tune vertical-specific guides via the /settings/style UI
# (lands later); `lifecycle.ignore_changes=[item]` (same pattern as
# pipeline-settings.tf) means re-applies do not revert operator edits.
# Per-PR env teardowns drop the rows with the table; the next env
# creation re-seeds with defaults.
#
# When the spec example in .ralph/specs/02-data-model.md § "Vertical
# Style Guide" changes, change BOTH the seed below and the
# corresponding pkg/style assertions.

resource "aws_dynamodb_table_item" "style_default" {
  table_name = aws_dynamodb_table.items.name
  hash_key   = aws_dynamodb_table.items.hash_key
  range_key  = aws_dynamodb_table.items.range_key

  item = <<-ITEM
  {
    "pk":       {"S": "STYLE#default"},
    "sk":       {"S": "PROFILE"},
    "type":     {"S": "VerticalStyleGuide"},
    "vertical": {"S": "default"},
    "tone":     {"S": "plain-English, specific, mild understatement; avoid hype"},
    "doPhrases": {"L": [
      {"S": "clear pricing"},
      {"S": "fixed-fee"},
      {"S": "no hidden fees"},
      {"S": "local to <area>"}
    ]},
    "dontPhrases": {"L": [
      {"S": "industry-leading"},
      {"S": "world-class"},
      {"S": "best-in-class"},
      {"S": "leverage"},
      {"S": "synergy"},
      {"S": "as you requested"}
    ]},
    "antiPatterns": {"L": [
      {"S": "stock photo of handshakes"},
      {"S": "AI-rendered staff portraits"},
      {"S": "generic team photo placeholder"}
    ]},
    "palette": {"M": {
      "primary": {"S": "#1F2937"},
      "neutral": {"L": [
        {"S": "#0F172A"},
        {"S": "#475569"},
        {"S": "#F1F5F9"}
      ]}
    }},
    "version":   {"N": "1"},
    "createdAt": {"S": "2026-05-14T00:00:00Z"},
    "updatedAt": {"S": "2026-05-14T00:00:00Z"},
    "etag":      {"S": "seed-default-v1"}
  }
  ITEM

  lifecycle {
    ignore_changes = [item]
  }
}

resource "aws_dynamodb_table_item" "style_accountants" {
  table_name = aws_dynamodb_table.items.name
  hash_key   = aws_dynamodb_table.items.hash_key
  range_key  = aws_dynamodb_table.items.range_key

  item = <<-ITEM
  {
    "pk":       {"S": "STYLE#accountants"},
    "sk":       {"S": "PROFILE"},
    "type":     {"S": "VerticalStyleGuide"},
    "vertical": {"S": "accountants"},
    "tone":     {"S": "professional, trustworthy, plain-English"},
    "doPhrases": {"L": [
      {"S": "clear pricing"},
      {"S": "fixed-fee accounting"},
      {"S": "Making Tax Digital"},
      {"S": "Xero / FreeAgent / QuickBooks"},
      {"S": "ICAEW / ACCA chartered"}
    ]},
    "dontPhrases": {"L": [
      {"S": "industry-leading"},
      {"S": "world-class"},
      {"S": "best-in-class"},
      {"S": "leverage"},
      {"S": "synergy"},
      {"S": "as you requested"},
      {"S": "save you a fortune"}
    ]},
    "antiPatterns": {"L": [
      {"S": "stock photo of handshakes"},
      {"S": "generic team photo placeholder"},
      {"S": "calculator-on-desk hero image"}
    ]},
    "palette": {"M": {
      "primary": {"S": "#0F4C81"},
      "neutral": {"L": [
        {"S": "#0F172A"},
        {"S": "#475569"},
        {"S": "#F1F5F9"}
      ]}
    }},
    "version":   {"N": "1"},
    "createdAt": {"S": "2026-05-14T00:00:00Z"},
    "updatedAt": {"S": "2026-05-14T00:00:00Z"},
    "etag":      {"S": "seed-accountants-v1"}
  }
  ITEM

  lifecycle {
    ignore_changes = [item]
  }
}
