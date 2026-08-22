# One ECR repository per deployment unit (T5-04; docs/SCAFFOLD_BOUNDARIES.md
# ARC-002/ARC-003/ARC-004/ARC-009). Repository names match the local image
# tags built by the root Dockerfile (url-shortener-<unit>).

locals {
  deployment_units = ["creation", "redirect", "admin", "worker"]
}

module "ecr" {
  source = "../../modules/ecr-repository"

  for_each = toset(local.deployment_units)

  repository_name = "url-shortener-${each.key}"
  tags            = module.tags.common_tags
}

output "ecr_repository_urls" {
  description = "Repository URL per deployment unit, for pushing/pulling images."
  value       = { for unit, repo in module.ecr : unit => repo.repository_url }
}
