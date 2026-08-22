variable "region" {
  description = <<-EOT
    AWS region for the state backend. No specific region has been
    decided anywhere in this project — docs/decisions/DEC-003.md only
    confirms "single-region", not which one. us-east-1 is used as a
    placeholder default (cheapest US region, no unusual constraints)
    until a real region decision is made. Change this before applying
    for real.
  EOT
  type        = string
  default     = "us-east-1"
}

variable "project" {
  description = "Short project identifier used in resource names."
  type        = string
  default     = "url-shortener"
}
