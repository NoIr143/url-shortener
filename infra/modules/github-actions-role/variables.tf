variable "role_name" {
  description = "Name for the IAM role GitHub Actions will assume."
  type        = string
}

variable "oidc_provider_arn" {
  description = "ARN of the account's GitHub Actions OIDC provider (infra/bootstrap)."
  type        = string
}

variable "github_repo" {
  description = "GitHub repository in \"owner/name\" form, e.g. \"NoIr143/url-shortener\"."
  type        = string
}

variable "allowed_subjects" {
  description = <<-EOT
    Exact OIDC subject claims allowed to assume this role, e.g.
    "repo:NoIr143/url-shortener:environment:dev" (only workflow runs
    deployed against the "dev" GitHub Environment) or
    "repo:NoIr143/url-shortener:ref:refs/heads/main" (only runs on main).
    This is the actual trust boundary (NFR-SEC-004) — keep it as narrow
    as the workflow that needs it, never a wildcard across the whole repo.
  EOT
  type        = list(string)
}

variable "policy_json" {
  description = "IAM policy document (JSON) granting exactly what the workflow needs — least privilege, not account-admin."
  type        = string
}

variable "tags" {
  type    = map(string)
  default = {}
}
