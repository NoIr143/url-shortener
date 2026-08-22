variable "repository_name" {
  description = "ECR repository name, e.g. \"url-shortener-creation\"."
  type        = string
}

variable "untagged_expiry_days" {
  description = "Days to keep an untagged (superseded) image before it expires."
  type        = number
  default     = 7
}

variable "max_tagged_images" {
  description = "Maximum number of images (any tag status) retained per repository."
  type        = number
  default     = 20
}

variable "tags" {
  type    = map(string)
  default = {}
}
