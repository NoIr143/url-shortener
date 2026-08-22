variable "name" {
  description = "Name prefix for VPC resources, e.g. \"url-shortener-dev\"."
  type        = string
}

variable "cidr_block" {
  description = "VPC CIDR block."
  type        = string
  default     = "10.0.0.0/16"
}

variable "azs" {
  description = "Availability zones to spread public/private subnets across (DEC-003: single-region, three-AZ)."
  type        = list(string)
}

variable "tags" {
  type    = map(string)
  default = {}
}
