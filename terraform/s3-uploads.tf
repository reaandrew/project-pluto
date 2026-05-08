# Per-env upload bucket — demonstrates the `force_destroy` pattern (pitfall #1).
# Production: NO force_destroy (manual gate). Preview envs: force_destroy=true so
# `terraform destroy` succeeds even with content. Cleanup script handles versioned
# objects + multipart uploads first.

resource "aws_s3_bucket" "uploads" {
  bucket        = "website-agency-uploads${local.env_suffix}-${data.aws_caller_identity.current.account_id}"
  force_destroy = !local.is_production

  tags = merge(local.common_tags, {
    Name      = "website-agency-uploads${local.env_suffix}"
    Component = "uploads"
  })
}

resource "aws_s3_bucket_versioning" "uploads" {
  bucket = aws_s3_bucket.uploads.id
  versioning_configuration {
    # Suspended in non-prod so cleanup is a one-shot delete; cleanup script still
    # handles versioned objects defensively.
    status = local.is_production ? "Enabled" : "Suspended"
  }
}

resource "aws_s3_bucket_public_access_block" "uploads" {
  bucket                  = aws_s3_bucket.uploads.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "uploads" {
  bucket = aws_s3_bucket.uploads.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}
