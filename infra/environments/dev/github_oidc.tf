# GitHub Actions role for the "dev" environment's Terraform-plan
# workflow (.github/workflows/terraform-plan.yml). Trust is scoped to
# `repo:<this repo>:environment:dev` only (NFR-SEC-004: "trust limited to
# repo/environment/workflow") — a workflow run that has not gone through
# the "dev" GitHub Environment cannot assume this role, and no other repo
# or branch can either.

variable "github_repo" {
  description = "GitHub repository in \"owner/name\" form."
  type        = string
  default     = "NoIr143/url-shortener"
}

variable "state_bucket" {
  description = "Must match infra/bootstrap's state bucket name (see infra/bootstrap/outputs.tf)."
  type        = string
  default     = "url-shortener-tofu-state"
}

variable "lock_table" {
  description = "Must match infra/bootstrap's lock table name."
  type        = string
  default     = "url-shortener-tofu-lock"
}

# Looked up by URL rather than via cross-state data, so this environment
# does not need read access to bootstrap's state file just to find one
# ARN — the OIDC provider URL is a fixed, well-known value once bootstrap
# has been applied once.
data "aws_iam_openid_connect_provider" "github_actions" {
  url = "https://token.actions.githubusercontent.com"
}

# Least-privilege: exactly what `tofu plan` needs against this
# environment's state — read/write the specific state object (plan
# doesn't write, but init does a read; kept minimal to state+lock only)
# and lock-table access. No permission to touch any actual AWS resource
# yet, because none exist (T5-05/06/07 add resources and, with them,
# whatever IAM permissions those specific resources require — additive,
# not granted in advance).
data "aws_iam_policy_document" "dev_plan" {
  statement {
    sid     = "StateObjectAccess"
    effect  = "Allow"
    actions = ["s3:GetObject", "s3:PutObject"]
    resources = [
      "arn:aws:s3:::${var.state_bucket}/url-shortener/dev/terraform.tfstate",
    ]
  }

  statement {
    sid       = "StateBucketList"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = ["arn:aws:s3:::${var.state_bucket}"]
  }

  statement {
    sid    = "LockTableAccess"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:DeleteItem",
    ]
    resources = ["arn:aws:dynamodb:*:*:table/${var.lock_table}"]
  }
}

module "dev_plan_role" {
  source = "../../modules/github-actions-role"

  role_name         = "url-shortener-dev-terraform-plan"
  oidc_provider_arn = data.aws_iam_openid_connect_provider.github_actions.arn
  github_repo       = var.github_repo
  allowed_subjects  = ["repo:${var.github_repo}:environment:dev"]
  policy_json       = data.aws_iam_policy_document.dev_plan.json
  tags              = module.tags.common_tags
}

output "dev_plan_role_arn" {
  value = module.dev_plan_role.role_arn
}
