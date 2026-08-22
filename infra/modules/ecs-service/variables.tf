variable "name" {
  description = "Service/task-family name, e.g. \"url-shortener-creation\"."
  type        = string
}

variable "cluster_id" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "image" {
  description = "Full image reference (repository URL + tag), e.g. an infra/modules/ecr-repository repository_url."
  type        = string
}

variable "container_port" {
  description = "Port the container listens on, or null for a service with no HTTP surface (e.g. the worker)."
  type        = number
  default     = null
}

variable "cpu" {
  type    = number
  default = 256
}

variable "memory" {
  type    = number
  default = 512
}

variable "desired_count" {
  type    = number
  default = 1
}

variable "subnet_ids" {
  description = "Private subnets to run tasks in."
  type        = list(string)
}

variable "ingress_security_group_ids" {
  description = "Security groups allowed to reach this service on container_port (typically an ALB's SG). Empty for a service with no inbound traffic."
  type        = list(string)
  default     = []
}

variable "environment" {
  type = list(object({
    name  = string
    value = string
  }))
  default = []
}

variable "listener_arn" {
  description = "ALB listener to attach a target group/rule to. null = no load balancer attachment (e.g. the worker)."
  type        = string
  default     = null
}

variable "listener_rule_priority" {
  type    = number
  default = null
}

variable "path_patterns" {
  description = "ALB listener-rule path patterns, e.g. [\"/\", \"/api/v1/urls\"]. Required when listener_arn is set."
  type        = list(string)
  default     = null
}

variable "health_check_path" {
  type    = string
  default = "/healthz"
}

variable "log_retention_days" {
  type    = number
  default = 14
}

variable "tags" {
  type    = map(string)
  default = {}
}
