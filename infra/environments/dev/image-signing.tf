# T5-07. Grants a CI-side identity permission to use the image-signing
# KMS key (kms.tf) — closing the gap flagged in
# docs/poc/T5-04-images-ecr.md: T5-04's signing demo used a throwaway
# local keypair. Trust is scoped to runs on `main` only (a released
# image is one that's actually merged), reusing the same
# github-actions-role module and exact-subject-match pattern T5-03
# established for the Terraform-plan role.
#
# This grants only the KMS actions — no workflow uses this role yet.
# Deciding when signing should actually happen (on merge to main? on a
# separate manual promotion step?) and wiring image-scan.yml or a new
# workflow to call `cosign sign --key awskms:///<key-id>` with it is
# deliberate follow-up work, not built here.

data "aws_iam_openid_connect_provider" "github_actions_signing" {
  url = "https://token.actions.githubusercontent.com"
}

data "aws_iam_policy_document" "image_signing" {
  statement {
    sid    = "SignWithImageSigningKey"
    effect = "Allow"
    actions = [
      "kms:Sign",
      "kms:GetPublicKey",
      "kms:DescribeKey",
    ]
    resources = [aws_kms_key.image_signing.arn]
  }
}

module "image_signing_role" {
  source = "../../modules/github-actions-role"

  role_name         = "url-shortener-dev-image-signing"
  oidc_provider_arn = data.aws_iam_openid_connect_provider.github_actions_signing.arn
  github_repo       = "NoIr143/url-shortener"
  allowed_subjects  = ["repo:NoIr143/url-shortener:ref:refs/heads/main"]
  policy_json       = data.aws_iam_policy_document.image_signing.json
  tags              = module.tags.common_tags
}

output "image_signing_role_arn" {
  value = module.image_signing_role.role_arn
}
