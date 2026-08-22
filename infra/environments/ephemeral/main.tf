# T5-08: the ephemeral-AWS integration testing mechanism, per
# docs/TECH_STACK.md Section 6: "Use ephemeral AWS test environments for
# transaction, IAM, ... behavior that local emulators cannot prove."
#
# Deliberately DynamoDB/IAM-scoped only — not the fuller VPC/ALB/ECS/edge
# stack. The internal/mapping and internal/keyalloc *_integration_test.go
# suites already create their own uniquely-named tables per test run
# (docs/poc/T5-08-local-compose-ephemeral-aws.md) and delete them in
# t.Cleanup, so this environment provisions NO DynamoDB tables itself —
# only the OIDC-trusted role those tests need to do that against real
# AWS. Testing WAF/autoscaling/failover (also named in TECH_STACK
# Section 6) needs the fuller stack and is a future extension of this
# environment, not built here — scope kept to what's immediately
# actionable without disproportionate growth for this task.

module "tags" {
  source = "../../modules/tags"

  project     = "url-shortener"
  environment = "ephemeral"
}

data "aws_caller_identity" "current" {}

data "aws_iam_openid_connect_provider" "github_actions" {
  url = "https://token.actions.githubusercontent.com"
}

# Wildcard resource patterns matching the exact test-table naming
# conventions already in the codebase (repo_*, backup_*, keyalloc_*) —
# this role can never touch the real "mapping"/"destination_claim"/
# "id_lease_counter" table names T5-07's task-role policies reference,
# even though both grant DynamoDB actions in the same account/region.
locals {
  test_table_arns = [
    "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/repo_*",
    "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/backup_*",
    "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/keyalloc_*",
  ]
}

# Unlike T5-07's application task-role policies (which deliberately
# exclude CreateTable — a long-lived task shouldn't have it), this role
# needs CreateTable/DeleteTable: its entire purpose is ephemeral table
# lifecycle for one CI run, not a standing application identity. The
# blast-radius argument that excludes it from creation/redirect's task
# roles doesn't apply the same way to a short-lived, workflow-scoped,
# OIDC-authenticated CI role resource-constrained to test-table names
# only.
data "aws_iam_policy_document" "ephemeral_dynamodb" {
  statement {
    sid    = "IntegrationTestTableLifecycle"
    effect = "Allow"
    actions = [
      "dynamodb:CreateTable",
      "dynamodb:DeleteTable",
      "dynamodb:DescribeTable",
      "dynamodb:PutItem",
      "dynamodb:GetItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:Scan",
      "dynamodb:TransactWriteItems",
      "dynamodb:TagResource",
    ]
    resources = local.test_table_arns
  }
}

module "ephemeral_ci_role" {
  source = "../../modules/github-actions-role"

  role_name         = "url-shortener-ephemeral-integration-ci"
  oidc_provider_arn = data.aws_iam_openid_connect_provider.github_actions.arn
  github_repo       = "NoIr143/url-shortener"
  allowed_subjects  = ["repo:NoIr143/url-shortener:ref:refs/heads/main"]
  policy_json       = data.aws_iam_policy_document.ephemeral_dynamodb.json
  tags              = module.tags.common_tags
}
