# One ECR repository for one deployment unit (docs/SCAFFOLD_BOUNDARIES.md:
# creation, redirect, admin, worker each build and ship a separate image —
# ARC-002/ARC-003/ARC-004/ARC-009). Reusable so the four repositories stay
# identical in policy rather than drifting.
#
# image_tag_mutability = IMMUTABLE is the actual mechanism behind this
# task's "immutable digest promoted" evidence bar: once a tag is pushed,
# ECR refuses to let that tag be overwritten, so a tag (and the digest it
# names) can only ever mean one build.

resource "aws_ecr_repository" "this" {
  name                 = var.repository_name
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }

  tags = var.tags
}

# Cost/maintainability control (NFR-MNT-001), not a security boundary:
# untagged images (superseded digests) expire quickly, and only the most
# recent N tagged images are kept per repository.
resource "aws_ecr_lifecycle_policy" "this" {
  repository = aws_ecr_repository.this.name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Expire untagged images after ${var.untagged_expiry_days} days"
        selection = {
          tagStatus   = "untagged"
          countType   = "sinceImagePushed"
          countUnit   = "days"
          countNumber = var.untagged_expiry_days
        }
        action = { type = "expire" }
      },
      {
        rulePriority = 2
        description  = "Keep only the most recent ${var.max_tagged_images} images"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = var.max_tagged_images
        }
        action = { type = "expire" }
      }
    ]
  })
}
