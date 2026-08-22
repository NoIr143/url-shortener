variable "domain_name" {
  description = <<-EOT
    Public apex domain for this environment, e.g. "short.example.com".
    Required — no default. No domain has been registered or chosen for
    this project yet (docs/research/T1-04-external-dependency-inventory.md
    explicitly says not to proceed with DNS/TLS/edge work assuming one);
    this variable must be supplied by whoever eventually applies this
    environment, never assumed here.
  EOT
  type        = string
}

variable "origin_domain_name" {
  description = "DNS name of the origin CloudFront forwards to (the public ALB's DNS name)."
  type        = string
}

variable "waf_rate_limit_per_5min" {
  description = "WAF rate-based rule threshold: requests per rolling 5-minute window per IP before that IP is blocked. Coarse, edge-level throttling (ARC-001) complementing the authoritative per-minute app-layer quotas already implemented in internal/api (docs/decisions/DEC-005.md). AWS WAFv2's minimum supported value is 100; this default is an unvalidated placeholder, not measured evidence."
  type        = number
  default     = 2000
}

variable "tags" {
  type    = map(string)
  default = {}
}
