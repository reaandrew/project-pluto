resource "aws_route53_zone" "website-agency" {
  name    = var.base_domain
  comment = "Hosted zone for website-agency app — delegated from ${var.parent_domain}"

  tags = {
    Name = var.base_domain
  }
}
