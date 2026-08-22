output "account_id" {
  description = "Confirms which AWS account this environment is actually pointed at."
  value       = data.aws_caller_identity.current.account_id
}

output "tags" {
  value = module.tags.common_tags
}
