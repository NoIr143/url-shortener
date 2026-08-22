# ECS/Fargate baseline (T5-05; ADR-012). One cluster, one service per
# deployment unit (docs/SCAFFOLD_BOUNDARIES.md), images from the T5-04
# ECR repositories. Container Insights intentionally left off — an
# added CloudWatch cost this cost-conscious, near-zero-traffic MVP
# (docs/COST_MODEL.md) doesn't need yet; enable it later if deeper
# per-task observability is actually needed.
resource "aws_ecs_cluster" "this" {
  name = "url-shortener-dev"
  tags = module.tags.common_tags
}

# Public ALB routing: an explicit path match for creation
# (root UI + POST /api/v1/urls, DEC-009), and an explicit catch-all for
# redirect (GET /{shortKey}) at lower priority. Nothing implicit — a
# request matching neither rule gets the ALB's own 404 default action,
# not a guess.
module "creation" {
  source = "../../modules/ecs-service"

  name           = "url-shortener-creation"
  cluster_id     = aws_ecs_cluster.this.id
  vpc_id         = module.vpc.vpc_id
  subnet_ids     = module.vpc.private_subnet_ids
  image          = "${module.ecr["creation"].repository_url}:latest"
  container_port = 8081

  ingress_security_group_ids = [module.public_alb.security_group_id]
  listener_arn               = module.public_alb.listener_arn
  listener_rule_priority     = 10
  path_patterns              = ["/", "/api/v1/urls"]
  health_check_path          = "/healthz"

  tags = module.tags.common_tags
}

module "redirect" {
  source = "../../modules/ecs-service"

  name           = "url-shortener-redirect"
  cluster_id     = aws_ecs_cluster.this.id
  vpc_id         = module.vpc.vpc_id
  subnet_ids     = module.vpc.private_subnet_ids
  image          = "${module.ecr["redirect"].repository_url}:latest"
  container_port = 8082

  ingress_security_group_ids = [module.public_alb.security_group_id]
  listener_arn               = module.public_alb.listener_arn
  listener_rule_priority     = 20
  path_patterns              = ["/*"]
  health_check_path          = "/healthz"

  tags = module.tags.common_tags
}

# Internal ALB only — admin has no rule, no security-group path, and no
# route on the public ALB at all (NFR-SEC-004: no public bypass).
#
# ADMIN_ADDR overrides cmd/admin's local-scaffold default of
# 127.0.0.1:8083 (loopback, unreachable from any real network hop) to
# ":8083" so the internal ALB — which reaches the task over its ENI, not
# loopback — can actually connect. The internal ALB's own restricted
# security group and lack of a public IP/DNS are what actually enforce
# "no public bypass" here, not the bind address.
module "admin" {
  source = "../../modules/ecs-service"

  name           = "url-shortener-admin"
  cluster_id     = aws_ecs_cluster.this.id
  vpc_id         = module.vpc.vpc_id
  subnet_ids     = module.vpc.private_subnet_ids
  image          = "${module.ecr["admin"].repository_url}:latest"
  container_port = 8083
  environment    = [{ name = "ADMIN_ADDR", value = ":8083" }]

  ingress_security_group_ids = [module.internal_alb.security_group_id]
  listener_arn               = module.internal_alb.listener_arn
  listener_rule_priority     = 10
  path_patterns              = ["/*"]
  health_check_path          = "/healthz"

  tags = module.tags.common_tags
}

# No ALB attachment at all — ARC-009's worker has no HTTP surface by
# design (background consumer), so it gets no target group, no listener
# rule, and no ingress security-group rule.
module "worker" {
  source = "../../modules/ecs-service"

  name       = "url-shortener-worker"
  cluster_id = aws_ecs_cluster.this.id
  vpc_id     = module.vpc.vpc_id
  subnet_ids = module.vpc.private_subnet_ids
  image      = "${module.ecr["worker"].repository_url}:latest"

  tags = module.tags.common_tags
}

output "public_alb_dns_name" {
  value = module.public_alb.alb_dns_name
}

output "internal_alb_dns_name" {
  value = module.internal_alb.alb_dns_name
}
