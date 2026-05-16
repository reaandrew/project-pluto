# SES — outreach domain, DNS records, verification & sandbox-out

The outbound sender is the dedicated subdomain **`outreach.<base_domain>`**
(never the corporate domain — see `.ralph/specs/06-discovery-and-compliance.md`
§ Domain warm-up). All SES infrastructure is in `terraform/ses.tf`
(created in iter 0.C.4); the identity, DKIM, and MAIL FROM resources are
**production-only singletons** (`count = local.is_production ? … : 0`)
because there is exactly one `outreach.<base_domain>` for the whole
project — per-PR envs do not register it.

`<base_domain>` and `<region>` are the Terraform `var.base_domain` /
`var.aws_region` for the production env (e.g. `agency.techar.ch`,
`eu-west-2`).

## What Terraform creates (no manual DNS needed)

`terraform/ses.tf` writes every required record into the parent Route53
zone automatically on the production apply:

| Record | Name | Type | Value |
|---|---|---|---|
| DKIM ×3 | `<token>._domainkey.outreach.<base_domain>` | CNAME | `<token>.dkim.amazonses.com` |
| MAIL FROM MX | `mail.outreach.<base_domain>` | MX | `10 feedback-smtp.<region>.amazonses.com` |
| MAIL FROM SPF | `mail.outreach.<base_domain>` | TXT | `v=spf1 include:amazonses.com -all` |

The SES email identity (`outreach.<base_domain>`), its configuration set
(`ai-website-agency-outreach<env_suffix>`), the MAIL FROM domain
(`mail.outreach.<base_domain>`, `behavior_on_mx_failure = REJECT_MESSAGE`),
and the bounce/complaint/delivery SNS topic + event destination are all
created by the same apply.

DKIM/SPF/MAIL-FROM verification then becomes "verified" automatically
once Route53 has propagated and SES has re-checked (minutes to a few
hours). No human DNS edit is required — this differs from the iter-0.A
ACM/NS bootstrap which does need manual NS records.

## DMARC (recommended, not Terraform-managed)

SES does not require DMARC, and we don't enforce it in Terraform to keep
the parent zone minimal. For deliverability, add this once, by hand, to
the parent zone when warming up:

```
_dmarc.outreach.<base_domain>  TXT  "v=DMARC1; p=none; rua=mailto:dmarc@<base_domain>"
```

Start at `p=none` (monitor only); tighten to `quarantine`/`reject` after
reviewing aggregate reports during warm-up.

## Verifying status

- **Operator UI**: `GET /email/status` (api-email Lambda, iter 8.1,
  operator-only) returns `verifiedForSending`, `dkimStatus`,
  `dkimSigningEnabled`, `mailFromDomain`, `mailFromDomainStatus` from
  `sesv2:GetEmailIdentity`. Surfaced on the Settings page. In a per-PR
  env (no prod identity) it degrades to
  `verifiedForSending:false / dkimStatus:UNKNOWN` rather than erroring.
- **CLI**:
  ```bash
  aws-vault exec <profile> -- aws sesv2 get-email-identity \
    --email-identity outreach.<base_domain> --region <region>
  ```

## Sandbox-out (one-time manual step)

A new SES account is in the **sandbox**: it can only send to verified
addresses and is rate-limited. Before iter 8 sends real outreach:

1. AWS Console → SES → **Account dashboard** → *Request production
   access*. Use case: low-volume B2B cold outreach for a UK web studio;
   describe the opt-out (`List-Unsubscribe` + reply line) and bounce/
   complaint handling (SNS → suppression).
2. Wait for approval (typically ~24h).
3. Confirm `outreach.<base_domain>` shows **Verified** and DKIM
   **Successful** (UI or CLI above) before flipping `outreachEnabled`.

This is operator/ops work — it is **not** automated and is a hard
prerequisite for iter 8.2 (`sender` Lambda) sending live email.
