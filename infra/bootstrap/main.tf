# One-time bootstrap for the OpenTofu remote state backend
# (docs/decisions ADR-015). Run this manually, once, before any
# environment under ../environments/ can use a remote backend:
#
#   cd infra/bootstrap
#   tofu init
#   tofu plan
#   tofu apply
#
# Then take the bucket/table names from `tofu output` and put them in
# ../environments/<env>/backend.hcl (see backend.hcl.example in that
# directory) before running `tofu init -backend-config=backend.hcl`
# there.

provider "aws" {
  region = var.region
}

# Encrypted, versioned state bucket. Versioning is the recovery
# mechanism for a corrupted/bad-apply state file; encryption and the
# public-access block are non-negotiable for a bucket holding
# infrastructure state (which can include resource IDs and, depending on
# what's ever added to the config, sensitive values).
resource "aws_s3_bucket" "state" {
  bucket = "${var.project}-tofu-state"

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "aws:kms"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_policy" "require_tls" {
  bucket = aws_s3_bucket.state.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource = [
        aws_s3_bucket.state.arn,
        "${aws_s3_bucket.state.arn}/*",
      ]
      Condition = {
        Bool = { "aws:SecureTransport" = "false" }
      }
    }]
  })
}

# Lock table: prevents two concurrent `tofu apply` runs (e.g., a CI run
# and a local run) from racing on the same state.
resource "aws_dynamodb_table" "lock" {
  name         = "${var.project}-tofu-lock"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }

  point_in_time_recovery {
    enabled = true
  }
}
