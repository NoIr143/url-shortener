variable "region" {
  description = "AWS region. See infra/bootstrap/variables.tf — no region has been formally decided (docs/decisions/DEC-003.md confirms only 'single-region')."
  type        = string
  default     = "us-east-1"
}

provider "aws" {
  region = var.region

  default_tags {
    tags = module.tags.common_tags
  }
}
