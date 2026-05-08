# Auto-delegation: parent andrewreaassociates.com zone is in the same AWS account, so we add
# the NS records for agency.andrewreaassociates.com automatically. This makes bootstrap a
# single `terraform apply` instead of the two-step apply-Ctrl-C-add-NS-apply
# dance described in tripwire/smm runbooks.
#
# If the parent zone moves out of this account, switch to manual delegation:
# delete this file, set the parent_zone_id variable to "", and follow the
# delegation_instructions output.

data "aws_route53_zone" "parent" {
  name         = var.parent_domain
  private_zone = false
}

resource "aws_route53_record" "delegation" {
  zone_id = data.aws_route53_zone.parent.zone_id
  name    = var.base_domain
  type    = "NS"
  ttl     = 300
  records = aws_route53_zone.website-agency.name_servers
}
