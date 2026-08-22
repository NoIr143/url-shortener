terraform {
  required_version = ">= 1.7.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # Intentionally local state for this module only. It creates the S3
  # bucket and DynamoDB table that every other environment's remote
  # backend depends on — there is nothing to point a remote backend at
  # until this has been applied once. Run this module manually,
  # infrequently, and keep terraform.tfstate for it safe (it is
  # gitignored, not committed).
}
