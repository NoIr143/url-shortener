output "ephemeral_ci_role_arn" {
  description = "Set as the EPHEMERAL_CI_ROLE_ARN repository variable once this environment has been applied for real."
  value       = module.ephemeral_ci_role.role_arn
}
