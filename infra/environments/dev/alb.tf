# ADR-012: public ALB (creation + redirect) and internal ALB (admin),
# separate services so admin has no public network path at all — not
# just an application-layer convention, an actual network boundary:
# the internal ALB has no public IP/DNS, and its security group only
# accepts traffic from inside the VPC, not 0.0.0.0/0.

module "public_alb" {
  source = "../../modules/alb"

  name                = "url-shortener-dev-public"
  vpc_id              = module.vpc.vpc_id
  subnet_ids          = module.vpc.public_subnet_ids
  internal            = false
  ingress_cidr_blocks = ["0.0.0.0/0"]
  tags                = module.tags.common_tags
}

module "internal_alb" {
  source = "../../modules/alb"

  name                = "url-shortener-dev-internal"
  vpc_id              = module.vpc.vpc_id
  subnet_ids          = module.vpc.private_subnet_ids
  internal            = true
  ingress_cidr_blocks = [module.vpc.vpc_cidr_block]
  tags                = module.tags.common_tags
}
