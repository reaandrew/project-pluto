# KMS CMK for passcode-cleartext encryption.
#
# Per `.ralph/specs/10-quality-rules.md` (non-negotiable) and iter 5.3 / 7.2:
# the 8-char Crockford-Base32 passcode for each generated preview is stored in
# DynamoDB **only as ciphertext** (`Website.passcodeCipher`) — encrypted with
# this CMK. The `publisher` Lambda generates + encrypts it; the `email-draft`
# Lambda decrypts it (within the cleartext-revealable window of 7 days)
# to embed in the outreach email.
#
# After iter 8.5's passcode-cleanup Lambda runs (24h post `email.sent`),
# `passcodeCipher` is wiped from the row — the CMK can no longer recover it.

resource "aws_kms_key" "passcode_cleartext" {
  description             = "Passcode cleartext envelope (publisher encrypt → email-draft decrypt → wiped 24h post-send) — env: ${var.environment}"
  deletion_window_in_days = local.is_production ? 30 : 7
  enable_key_rotation     = true
  rotation_period_in_days = 365

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      # Standard root-account permission so AWS-managed key administration works.
      {
        Sid       = "EnableRootAccess"
        Effect    = "Allow"
        Principal = { AWS = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:root" }
        Action    = "kms:*"
        Resource  = "*"
      },
      # Encrypt/decrypt for the shared Lambda execution role (publisher reads
      # via Encrypt+GenerateDataKey; email-draft via Decrypt). Cipher operations
      # only — the role can't manage or schedule deletion of the key.
      {
        Sid       = "AllowLambdaEncryptDecrypt"
        Effect    = "Allow"
        Principal = { AWS = aws_iam_role.lambda_api.arn }
        Action = [
          "kms:Encrypt",
          "kms:Decrypt",
          "kms:GenerateDataKey",
          "kms:DescribeKey",
        ]
        Resource = "*"
      },
    ]
  })

  tags = merge(local.common_tags, {
    Name      = "passcode-cleartext${local.env_suffix}"
    Component = "passcode-encryption"
  })
}

resource "aws_kms_alias" "passcode_cleartext" {
  name          = "alias/passcode-cleartext${local.env_suffix}"
  target_key_id = aws_kms_key.passcode_cleartext.key_id
}

# Surface the alias ARN via SSM so Lambdas can resolve it at boot without needing
# the key ID in their env vars (key rotates → ARN is stable, alias is stable).
resource "aws_ssm_parameter" "passcode_cmk_alias_arn" {
  name  = "/ai-website-agency/${var.environment}/kms/passcode_cleartext_alias_arn"
  type  = "String"
  value = aws_kms_alias.passcode_cleartext.arn
  tags  = local.common_tags
}

output "passcode_cmk_alias_arn" {
  value       = aws_kms_alias.passcode_cleartext.arn
  description = "KMS alias ARN for passcode-cleartext envelope encryption (used by publisher + email-draft)"
}
