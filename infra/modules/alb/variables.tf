variable "name" {
  description = "ALB name, e.g. \"url-shortener-dev-public\"."
  type        = string
}

variable "vpc_id" {
  type = string
}

variable "subnet_ids" {
  description = "Subnets for the ALB itself — public subnets for an internet-facing ALB, private subnets for an internal one."
  type        = list(string)
}

variable "internal" {
  description = "true = internal ALB (no public DNS/IP, reachable only from inside the VPC); false = internet-facing."
  type        = bool
}

variable "ingress_cidr_blocks" {
  description = "CIDR blocks allowed to reach this ALB on port 80. [\"0.0.0.0/0\"] for the public ALB; the VPC's own CIDR for the internal ALB (NFR-SEC-004: no public bypass)."
  type        = list(string)
}

variable "tags" {
  type    = map(string)
  default = {}
}
