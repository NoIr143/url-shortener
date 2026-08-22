# T5-07. No current task provisions ElastiCache/Valkey (flagged in
# docs/poc/T5-05-vpc-alb-ecs.md), so there is no real AUTH token or
# other credential to store yet. This secret is deliberately created as
# an empty container only — no aws_secretsmanager_secret_version, no
# fabricated placeholder value — to demonstrate the real mechanism
# (KMS-encrypted, least-privilege IAM grant scoped to one secret ARN)
# without inventing fake secret content. Populate it for real once a
# concrete credential exists to protect.
resource "aws_secretsmanager_secret" "valkey_auth_token" {
  name       = "url-shortener/dev/valkey-auth-token"
  kms_key_id = aws_kms_key.secrets.arn
  tags       = module.tags.common_tags
}

data "aws_iam_policy_document" "valkey_secret_read" {
  statement {
    sid       = "ReadValkeyAuthTokenSecret"
    effect    = "Allow"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.valkey_auth_token.arn]
  }
}

# Granted to creation and redirect only — the two real services that
# would actually connect to a cache/data layer. admin/worker remain
# untouched (nothing in their scaffold code reads a secret yet).
resource "aws_iam_role_policy" "creation_read_valkey_secret" {
  name   = "read-valkey-auth-token"
  role   = module.creation.task_role_name
  policy = data.aws_iam_policy_document.valkey_secret_read.json
}

resource "aws_iam_role_policy" "redirect_read_valkey_secret" {
  name   = "read-valkey-auth-token"
  role   = module.redirect.task_role_name
  policy = data.aws_iam_policy_document.valkey_secret_read.json
}
