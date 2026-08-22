# Standard tag set every resource in this project should carry. This is
# deliberately the first real module: small, immediately useful, and it
# proves the environments/ -> modules/ calling pattern works end to end
# (verified by `tofu plan` in environments/dev) without inventing
# network/compute modules ahead of the tasks (T5-05/06/07) that actually
# need them.

locals {
  common_tags = {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "opentofu"
  }
}
