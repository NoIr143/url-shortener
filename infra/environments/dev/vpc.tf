# DEC-003: single-region, three-AZ. Picked dynamically rather than
# hardcoded so this doesn't silently break if the region variable
# (infra/environments/dev/providers.tf) ever changes.
data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 3)
}

module "vpc" {
  source = "../../modules/vpc"

  name       = "url-shortener-dev"
  cidr_block = "10.0.0.0/16"
  azs        = local.azs
  tags       = module.tags.common_tags
}
