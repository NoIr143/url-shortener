# One Application Load Balancer, HTTP-only for now — TLS/ACM/CloudFront
# is T5-06's scope, not this task's. Used twice in infra/environments/dev
# (ADR-012): once internet-facing for creation+redirect, once internal
# for admin, with `internal` + `ingress_cidr_blocks` set differently by
# the caller so the two get genuinely different network exposure, not
# just different names.
#
# The default listener action is a static 404, not a forward to any
# service — every real route is an explicit listener rule added by the
# caller (infra/environments/dev/ecs.tf). An unmatched path fails closed
# instead of guessing a destination, consistent with this project's
# "no redirect from unknown/ambiguous state" rule.

resource "aws_security_group" "this" {
  name_prefix = "${var.name}-alb-"
  vpc_id      = var.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-alb" })

  ingress {
    description = "HTTP"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = var.ingress_cidr_blocks
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_lb" "this" {
  name               = var.name
  internal           = var.internal
  load_balancer_type = "application"
  security_groups    = [aws_security_group.this.id]
  subnets            = var.subnet_ids

  tags = var.tags
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "fixed-response"

    fixed_response {
      content_type = "text/plain"
      message_body = "no route"
      status_code  = "404"
    }
  }
}
