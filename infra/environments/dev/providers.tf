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

# CloudFront's ACM certificate and its WAFv2 Web ACL (scope=CLOUDFRONT)
# must both live in us-east-1 regardless of var.region — pinned
# explicitly here (infra/modules/edge/) rather than relying on
# var.region happening to already be us-east-1, so this doesn't
# silently break if the region decision above ever changes.
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"

  default_tags {
    tags = module.tags.common_tags
  }
}
