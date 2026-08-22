terraform {
  required_version = ">= 1.7.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # Remote state (ADR-015). Bucket/table/region come from
  # backend.hcl (gitignored — generated from infra/bootstrap's outputs,
  # see backend.hcl.example), not hardcoded here, so this file doesn't
  # need editing per environment or leak account-specific names into
  # version control.
  backend "s3" {
    key     = "url-shortener/dev/terraform.tfstate"
    encrypt = true
  }
}
