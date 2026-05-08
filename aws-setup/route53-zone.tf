resource "aws_route53_zone" "ai-website-agency" {
  name    = var.base_domain
  comment = "Hosted zone for ai-website-agency app — delegated from ${var.parent_domain}"

  tags = {
    Name = var.base_domain
  }
}
