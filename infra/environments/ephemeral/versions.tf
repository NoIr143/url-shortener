terraform {
  required_version = ">= 1.7.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # Same remote backend (bucket/table) as environments/dev
  # (infra/bootstrap) — no `key` here, deliberately: T5-08's workflow
  # supplies a per-run key (-backend-config="key=...${{ github.run_id }}")
  # so concurrent ephemeral runs never collide on the same state file.
  backend "s3" {
    encrypt = true
  }
}
