# GitHub Actions OIDC identity provider (ADR-015: "no long-lived AWS
# key"). This is an AWS-account-level singleton — only one OIDC provider
# can exist per issuer URL per account — so it belongs in bootstrap
# alongside the other one-time account setup, not duplicated per
# environment. Environment-specific trust (which repo/branch/environment
# may assume which role) is configured separately in each environment via
# infra/modules/github-actions-role (see infra/environments/dev/github_oidc.tf).

# GitHub's OIDC thumbprint is well-known and documented by GitHub itself;
# AWS also validates the certificate chain independently, so this
# thumbprint is a defense-in-depth pin, not the sole trust mechanism.
# https://docs.github.com/en/actions/deployment/security-hardening-your-deployments/about-security-hardening-with-openid-connect
resource "aws_iam_openid_connect_provider" "github_actions" {
  url = "https://token.actions.githubusercontent.com"

  client_id_list = [
    "sts.amazonaws.com",
  ]

  thumbprint_list = [
    "6938fd4d98bab03faadb97b34396831e3780aea1", # DigiCert Global Root CA
  ]
}

output "github_oidc_provider_arn" {
  value = aws_iam_openid_connect_provider.github_actions.arn
}
