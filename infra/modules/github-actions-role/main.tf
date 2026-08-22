# Reusable role for one GitHub Actions workflow/environment to assume via
# OIDC, no long-lived AWS key stored as a GitHub secret (ADR-015).
#
# The trust policy is scoped two ways, both required:
#   1. `aud` must be "sts.amazonaws.com" (standard OIDC audience check).
#   2. `sub` must exactly match one of var.allowed_subjects — not a
#      prefix/wildcard match. A workflow run from a fork, a different
#      branch, or a different GitHub Environment cannot assume this role,
#      even though it authenticates against the same OIDC provider.

data "aws_iam_policy_document" "trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [var.oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = var.allowed_subjects
    }
  }
}

resource "aws_iam_role" "this" {
  name               = var.role_name
  assume_role_policy = data.aws_iam_policy_document.trust.json
  tags               = var.tags
}

resource "aws_iam_role_policy" "this" {
  name   = "${var.role_name}-policy"
  role   = aws_iam_role.this.id
  policy = var.policy_json
}
