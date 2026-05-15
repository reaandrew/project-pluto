# Iter 7.1 — seed EmailToneProfile rows in the items table.
#
# Two seed rows: "default" (the fallback the email-draft Lambda reads
# when no vertical-specific profile exists) and "accountants" (the
# project's first concrete vertical — mirrors the spec example +
# pkg/tone test fixtures).
#
# Operators tune vertical-specific profiles via the /settings/email-tone
# UI (lands later) and the email-tone-tuner proposes deltas;
# `lifecycle.ignore_changes=[item]` (same pattern as style-guides.tf /
# pipeline-settings.tf) means re-applies do not revert operator/tuner
# edits. Per-PR env teardowns drop the rows with the table; the next
# env creation re-seeds with defaults.
#
# When the spec example in .ralph/specs/02-data-model.md §
# "EmailToneProfile" changes, change BOTH the seed below and the
# corresponding pkg/tone assertions. prohibitedPhrases always carries
# the project-wide exaggeration ban + "password" (the email.v1
# post-validator separately rejects the word "password"; keeping it in
# the list documents intent and the tuner can extend it per vertical).

resource "aws_dynamodb_table_item" "email_tone_default" {
  table_name = aws_dynamodb_table.items.name
  hash_key   = aws_dynamodb_table.items.hash_key
  range_key  = aws_dynamodb_table.items.range_key

  item = <<-ITEM
  {
    "pk":       {"S": "EMAIL_TONE#default"},
    "sk":       {"S": "PROFILE"},
    "type":     {"S": "EmailToneProfile"},
    "vertical": {"S": "default"},
    "subjectPatterns": {"L": [
      {"S": "Quick redesign preview for {{businessName}}"},
      {"S": "Mocked up an alternate site for {{businessName}}"}
    ]},
    "openerPatterns": {"L": [
      {"S": "Hi {{firstName}},"},
      {"S": "Hi {{firstName}} — saw {{businessName}} listed in {{location}}."}
    ]},
    "prohibitedPhrases": {"L": [
      {"S": "password"},
      {"S": "industry-leading"},
      {"S": "world-class"},
      {"S": "best-in-class"},
      {"S": "leverage"},
      {"S": "synergy"},
      {"S": "as you requested"}
    ]},
    "signature":  {"S": "Andrew\nAndrew Rea Associates\nhttps://techar.ch"},
    "optOutLine": {"S": "Reply 'no thanks' and I won't follow up."},
    "version":    {"N": "1"},
    "createdAt":  {"S": "2026-05-15T00:00:00Z"},
    "updatedAt":  {"S": "2026-05-15T00:00:00Z"},
    "etag":       {"S": "seed-default-v1"}
  }
  ITEM

  lifecycle {
    ignore_changes = [item]
  }
}

resource "aws_dynamodb_table_item" "email_tone_accountants" {
  table_name = aws_dynamodb_table.items.name
  hash_key   = aws_dynamodb_table.items.hash_key
  range_key  = aws_dynamodb_table.items.range_key

  item = <<-ITEM
  {
    "pk":       {"S": "EMAIL_TONE#accountants"},
    "sk":       {"S": "PROFILE"},
    "type":     {"S": "EmailToneProfile"},
    "vertical": {"S": "accountants"},
    "subjectPatterns": {"L": [
      {"S": "Quick redesign preview for {{businessName}}"},
      {"S": "Reworked the {{businessName}} site — fixed-fee focus"}
    ]},
    "openerPatterns": {"L": [
      {"S": "Hi {{firstName}},"},
      {"S": "Hi {{firstName}} — came across {{businessName}} while looking at accountants in {{location}}."}
    ]},
    "prohibitedPhrases": {"L": [
      {"S": "password"},
      {"S": "industry-leading"},
      {"S": "world-class"},
      {"S": "best-in-class"},
      {"S": "leverage"},
      {"S": "synergy"},
      {"S": "as you requested"},
      {"S": "save you a fortune"}
    ]},
    "signature":  {"S": "Andrew\nAndrew Rea Associates\nhttps://techar.ch"},
    "optOutLine": {"S": "Reply 'no thanks' and I won't follow up."},
    "version":    {"N": "1"},
    "createdAt":  {"S": "2026-05-15T00:00:00Z"},
    "updatedAt":  {"S": "2026-05-15T00:00:00Z"},
    "etag":       {"S": "seed-accountants-v1"}
  }
  ITEM

  lifecycle {
    ignore_changes = [item]
  }
}
