# Route 53 + ACM + CloudFront + WAF (T5-06; ARC-001). Origin is the
# public ALB (T5-05) — redirect and creation traffic goes through the
# edge; the internal ALB (admin) is never reachable from here or from
# anywhere else outside the VPC.
module "edge" {
  source = "../../modules/edge"

  providers = {
    aws           = aws
    aws.us_east_1 = aws.us_east_1
  }

  domain_name        = var.domain_name
  origin_domain_name = module.public_alb.alb_dns_name

  tags = module.tags.common_tags
}

output "cloudfront_distribution_domain_name" {
  value = module.edge.distribution_domain_name
}

output "route53_name_servers" {
  description = "Set these as the domain's NS records at the registrar once this is applied."
  value       = module.edge.name_servers
}
