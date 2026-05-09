# Pipeline blobs bucket — fetched HTML, screenshots, generated site bundles
# (until they're shipped to R2 by iter 5's publisher), Bedrock raw responses
# (cached separately in DDB but full-text body lives here for replay).
#
# Per-env (preview envs need to destroy on PR close — pitfall #1: force_destroy).

resource "aws_s3_bucket" "pipeline_blobs" {
  bucket        = "ai-website-agency-blobs${local.env_suffix}-${data.aws_caller_identity.current.account_id}"
  force_destroy = !local.is_production

  tags = merge(local.common_tags, {
    Name      = "ai-website-agency-blobs${local.env_suffix}"
    Component = "pipeline-blobs"
  })
}

resource "aws_s3_bucket_versioning" "pipeline_blobs" {
  bucket = aws_s3_bucket.pipeline_blobs.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "pipeline_blobs" {
  bucket = aws_s3_bucket.pipeline_blobs.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "pipeline_blobs" {
  bucket                  = aws_s3_bucket.pipeline_blobs.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Lifecycle: 90-day transition to IA, 365-day to Glacier, 1825-day expiry.
# Bedrock raw responses + screenshots are large and rarely re-read after their
# day-of-pipeline use; lifecycle keeps storage cost bounded.
resource "aws_s3_bucket_lifecycle_configuration" "pipeline_blobs" {
  bucket = aws_s3_bucket.pipeline_blobs.id

  rule {
    id     = "tier-and-expire"
    status = "Enabled"

    filter {} # apply to all objects

    transition {
      days          = 90
      storage_class = "STANDARD_IA"
    }
    transition {
      days          = 365
      storage_class = "GLACIER"
    }
    expiration {
      days = 1825 # 5 years
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  rule {
    id     = "expire-noncurrent-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = 30
    }
  }
}

# Lambdas (the audit + generator + publisher chain) need read/write on this bucket.
resource "aws_iam_role_policy" "blobs_rw" {
  name = "blobs-rw"
  role = aws_iam_role.lambda_api.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:ListBucket",
      ]
      Resource = [
        aws_s3_bucket.pipeline_blobs.arn,
        "${aws_s3_bucket.pipeline_blobs.arn}/*",
      ]
    }]
  })
}

output "pipeline_blobs_bucket" {
  value       = aws_s3_bucket.pipeline_blobs.id
  description = "S3 bucket for pipeline blobs (HTML, screenshots, bundles, raw Bedrock responses)"
}
