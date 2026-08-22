# T5-07: real least-privilege task-role policies, replacing the empty
# roles T5-05 deliberately left as placeholders. Grounded in the actual
# DynamoDB calls each service's Go code makes (internal/mapping,
# internal/keyalloc), not a blanket policy:
#
#   - creation (cmd/creation): Create() commits mapping+destination_claim
#     in one TransactWriteItems (internal/mapping/repository.go), and
#     falls back to GetItem on destination_claim when the transaction is
#     canceled by an exact-repeat. Key allocation (internal/keyalloc)
#     does PutItem/GetItem/UpdateItem on id_lease_counter only.
#   - redirect (cmd/redirect): only ever calls Get() -> GetItem on the
#     mapping table. It never touches destination_claim or
#     id_lease_counter — it doesn't import internal/keyalloc at all
#     (docs/SCAFFOLD_BOUNDARIES.md).
#
# No table name here has a matching aws_dynamodb_table resource yet — no
# current task provisions DynamoDB via IaC (flagged in
# docs/poc/T5-05-vpc-alb-ecs.md). These ARNs are constructed against the
# exact table names the Go code already hardcodes ("mapping",
# "destination_claim", "id_lease_counter"), so the grant is correct the
# moment those tables are created, in whatever future task does that.
#
# dynamodb:CreateTable is deliberately NOT granted, even though
# EnsureTables (both repository.go and allocator.go) calls it on
# startup. Runtime application code creating tables is a blast-radius
# risk a compromised task shouldn't have, and table provisioning
# belongs in IaC, not app code, in a real deployment. This means
# EnsureTables would fail with AccessDenied against real AWS today —
# an honest current gap, not silently patched here.

locals {
  mapping_table_arn           = "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/mapping"
  destination_claim_table_arn = "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/destination_claim"
  id_lease_counter_table_arn  = "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/id_lease_counter"
}

data "aws_iam_policy_document" "creation_dynamodb" {
  statement {
    sid    = "MappingAndClaimTransactionalWrite"
    effect = "Allow"
    actions = [
      "dynamodb:TransactWriteItems",
      "dynamodb:PutItem",
      "dynamodb:GetItem",
    ]
    resources = [
      local.mapping_table_arn,
      local.destination_claim_table_arn,
    ]
  }

  statement {
    sid    = "KeyLeaseAllocation"
    effect = "Allow"
    actions = [
      "dynamodb:PutItem",
      "dynamodb:GetItem",
      "dynamodb:UpdateItem",
    ]
    resources = [local.id_lease_counter_table_arn]
  }
}

resource "aws_iam_role_policy" "creation_dynamodb" {
  name   = "dynamodb-least-privilege"
  role   = module.creation.task_role_name
  policy = data.aws_iam_policy_document.creation_dynamodb.json
}

data "aws_iam_policy_document" "redirect_dynamodb" {
  statement {
    sid       = "MappingRead"
    effect    = "Allow"
    actions   = ["dynamodb:GetItem"]
    resources = [local.mapping_table_arn]
  }
}

resource "aws_iam_role_policy" "redirect_dynamodb" {
  name   = "dynamodb-least-privilege"
  role   = module.redirect.task_role_name
  policy = data.aws_iam_policy_document.redirect_dynamodb.json
}
