output "state_bucket" {
  description = "S3 bucket name — put this in environments/<env>/backend.hcl."
  value       = aws_s3_bucket.state.id
}

output "lock_table" {
  description = "DynamoDB lock table name — put this in environments/<env>/backend.hcl."
  value       = aws_dynamodb_table.lock.name
}

output "region" {
  value = var.region
}
