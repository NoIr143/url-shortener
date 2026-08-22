provider "aws" {
  region = var.region

  default_tags {
    tags = module.tags.common_tags
  }
}
