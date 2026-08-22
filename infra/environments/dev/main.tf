# dev environment root module.
#
# T5-02 scope is the remote-state/root-environment scaffold itself
# (docs/TASK_BREAKDOWN.md), not the application infrastructure — VPC and
# compute (T5-05), edge/DNS/TLS (T5-06), and security services (T5-07)
# are separate tasks and deliberately not built here yet. This file
# currently only proves the environments/ -> modules/ wiring and the
# remote-state pipeline work, via one real (free) data source and the
# tags module.

module "tags" {
  source = "../../modules/tags"

  project     = "url-shortener"
  environment = "dev"
}

# Zero-cost, read-only: proves the provider/backend/module pipeline is
# genuinely wired to a real AWS account, without creating anything.
data "aws_caller_identity" "current" {}
