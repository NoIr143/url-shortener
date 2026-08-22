# Route 53 + ACM + CloudFront + WAF baseline (T5-06; ARC-001 Edge
# Gateway: "TLS termination, routing, request size limits, coarse
# rate/WAF policy, correlation ID; no mapping source data ... fail
# closed on invalid route; preserve path case").
#
# Route 53 hosted zone is created here as a resource, not looked up as
# an existing one — this assumes Route 53 will be used as the DNS host
# once a domain exists (consistent with docs/TECH_STACK.md's AWS-centric
# proposal), independent of which registrar the domain itself is bought
# through. If a hosted zone already exists elsewhere, this needs
# adjusting to a data source instead — flagged, not resolved here, since
# no domain decision has been made either way.

resource "aws_route53_zone" "this" {
  name = var.domain_name
  tags = var.tags
}

resource "aws_acm_certificate" "this" {
  provider = aws.us_east_1

  domain_name       = var.domain_name
  validation_method = "DNS"
  tags              = var.tags

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "cert_validation" {
  for_each = {
    for dvo in aws_acm_certificate.this.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      record = dvo.resource_record_value
      type   = dvo.resource_record_type
    }
  }

  zone_id         = aws_route53_zone.this.zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "this" {
  provider = aws.us_east_1

  certificate_arn         = aws_acm_certificate.this.arn
  validation_record_fqdns = [for r in aws_route53_record.cert_validation : r.fqdn]
}

# Coarse edge-level protections, complementing (not replacing)
# internal/api's authoritative per-minute app-layer rate limiter
# (docs/decisions/DEC-005.md). The rate-based rule actively blocks —
# that's the point of a floor-level throttle. The two generic AWS
# managed rule groups start in COUNT (observe-only) mode deliberately:
# this project has never run a penetration test or collected
# false-positive evidence against the real creation/redirect endpoints
# (docs/SRS.md G-007), so defaulting straight to BLOCK risks silently
# dropping legitimate traffic with no evidence it's safe to do so.
# Switch to BLOCK once that evidence exists.
resource "aws_wafv2_web_acl" "this" {
  provider = aws.us_east_1

  name  = "${replace(var.domain_name, ".", "-")}-edge"
  scope = "CLOUDFRONT"

  default_action {
    allow {}
  }

  rule {
    name     = "rate-limit-per-ip"
    priority = 0

    action {
      block {}
    }

    statement {
      rate_based_statement {
        limit              = var.waf_rate_limit_per_5min
        aggregate_key_type = "IP"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "rate-limit-per-ip"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "aws-common-rule-set"
    priority = 1

    override_action {
      count {}
    }

    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "aws-common-rule-set"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "aws-known-bad-inputs"
    priority = 2

    override_action {
      count {}
    }

    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
        vendor_name = "AWS"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "aws-known-bad-inputs"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "edge-web-acl"
    sampled_requests_enabled   = true
  }

  tags = var.tags
}

# Caching is intentionally disabled (AWS managed "CachingDisabled"
# policy) for every response through this distribution. A redirect can
# transition Active -> Suspended and must reflect that within the
# 60-second propagation target (docs/decisions/DEC-008.md), which is
# already coordinated end-to-end between the redirect cache TTL
# (ARC-007/Valkey) and its invalidation event. An independent CDN-level
# cache with its own TTL would be a second, uncoordinated staleness
# source stacked on top of that budget. Revisit only alongside an
# explicit, coordinated caching design for this specific safety
# property — not as a generic performance optimization.
#
# origin_protocol_policy = "http-only": the public ALB (T5-05) has no
# HTTPS listener yet, so this hop is plain HTTP over the AWS backbone,
# not the public internet. The only hop that actually crosses the
# public internet (viewer -> CloudFront) is full TLS via the ACM
# certificate below. Closing this gap needs a second, regional ACM
# certificate and an HTTPS ALB listener — not built here; flagged as a
# residual gap, not a silent shortcut.
resource "aws_cloudfront_distribution" "this" {
  enabled     = true
  aliases     = [var.domain_name]
  price_class = "PriceClass_100"
  web_acl_id  = aws_wafv2_web_acl.this.arn
  depends_on  = [aws_acm_certificate_validation.this]

  origin {
    domain_name = var.origin_domain_name
    origin_id   = "public-alb"

    custom_origin_config {
      origin_protocol_policy = "http-only"
      http_port              = 80
      https_port             = 443
      origin_ssl_protocols   = ["TLSv1.2"]
    }
  }

  default_cache_behavior {
    allowed_methods        = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods         = ["GET", "HEAD"]
    target_origin_id       = "public-alb"
    viewer_protocol_policy = "redirect-to-https"

    cache_policy_id          = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad" # AWS managed "CachingDisabled"
    origin_request_policy_id = "216adef6-5c7f-47e4-b989-5492eafa07d3" # AWS managed "AllViewer"
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    acm_certificate_arn      = aws_acm_certificate_validation.this.certificate_arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }

  tags = var.tags
}

resource "aws_route53_record" "apex" {
  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain_name
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.this.domain_name
    zone_id                = aws_cloudfront_distribution.this.hosted_zone_id
    evaluate_target_health = false
  }
}
