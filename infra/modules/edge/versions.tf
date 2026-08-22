terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
      # CloudFront's ACM certificate and its WAFv2 Web ACL (scope =
      # CLOUDFRONT) must both live in us-east-1 regardless of the
      # environment's own default region. The caller must pass both the
      # default `aws` provider and one aliased `aws.us_east_1`.
      configuration_aliases = [aws.us_east_1]
    }
  }
}
