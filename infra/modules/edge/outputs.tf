output "distribution_id" {
  value = aws_cloudfront_distribution.this.id
}

output "distribution_domain_name" {
  value = aws_cloudfront_distribution.this.domain_name
}

output "hosted_zone_id" {
  value = aws_route53_zone.this.zone_id
}

output "name_servers" {
  description = "Set these as the domain's NS records at the registrar once this is applied — otherwise Route 53 is not authoritative for the domain and nothing here actually resolves."
  value       = aws_route53_zone.this.name_servers
}

output "certificate_arn" {
  value = aws_acm_certificate_validation.this.certificate_arn
}

output "web_acl_arn" {
  value = aws_wafv2_web_acl.this.arn
}
