# 02 — Data Model (DynamoDB single-table)

## Table

The skeleton already provisions `<project>-items[-<env>]` (e.g. `agency-items` for production, `agency-items-feat-x` for a preview env) in `terraform/dynamodb-items.tf`. We extend it with three GSIs; we do not rename it.

- Billing: `PAY_PER_REQUEST` (skeleton default).
- Hash: `pk`. Range: `sk`.
- TTL attribute: **`expires_at`** (skeleton already wires this — do not introduce a second TTL attribute).
- Streams: enable `NEW_AND_OLD_IMAGES` in our extension.
- PITR + deletion-protection: prod-only (skeleton pitfall #2 — leave the `local.is_production` gate intact).

GSIs (added by us in `terraform/dynamodb-items-gsis.tf`, sparse, projected `KEYS_ONLY` unless noted):
- `gsi1` (`gsi1pk`, `gsi1sk`) — status-based access (queue listings, backlog ordering).
- `gsi2` (`gsi2pk`, `gsi2sk`) — vertical-based access (analytics, tuner aggregations).
- `gsi3` (`gsi3pk`, `gsi3sk`) — domain-uniqueness lookup.

## Item shapes

All items have `type`, `createdAt`, `updatedAt`, `etag` (`uuid` mutated on each write — used for optimistic concurrency in the API layer). Items that auto-expire set `expires_at` to a Unix timestamp; the skeleton's TTL attribute drives expiry.

### Pipeline Settings (singleton)

```json
{
  "pk": "SETTINGS#GLOBAL",
  "sk": "PROFILE",
  "type": "PipelineSettings",
  "pipelineEnabled": true,
  "stages": {
    "discoveryEnabled": true,
    "auditEnabled": true,
    "previewEnabled": true,
    "outreachEnabled": false
  },
  "caps": {
    "maxDiscoveriesPerDay": 100,
    "maxAuditsPerDay": 50,
    "maxPreviewsPerDay": 10,
    "maxEmailsPerDay": 5,
    "maxReviewQueueSize": 20,
    "maxBacklogSize": 500
  },
  "thresholds": {
    "minTechnicalIssueScore": 30,
    "minQualificationScore": 70,
    "minContactConfidence": 0.6
  },
  "budgets": {
    "dailyBedrockUsd": 5,
    "dailyPlacesUsd": 2,
    "dailyEmailUsd": 1
  },
  "createdAt": "...",
  "updatedAt": "...",
  "etag": "..."
}
```

### Targeting Profile

```json
{
  "pk": "TARGET#<id>",
  "sk": "PROFILE",
  "type": "TargetingProfile",
  "id": "<uuid>",
  "vertical": "accountants",
  "location": "Manchester, UK",
  "includeKeywords": ["chartered", "tax"],
  "excludeKeywords": ["franchise"],
  "weights": {
    "websiteAge": 0.2,
    "auditScore": 0.3,
    "businessSize": 0.2,
    "contactConfidence": 0.2,
    "verticalFit": 0.1
  },
  "enabled": true,
  "lastRunAt": "...",
  "stats": { "discovered7d": 0, "qualified7d": 0, "approved7d": 0 },
  "createdAt": "...",
  "updatedAt": "...",
  "etag": "...",
  "gsi1pk": "TARGET#ENABLED",
  "gsi1sk": "<vertical>#<location>"
}
```

### Vertical Style Guide

```json
{
  "pk": "STYLE#<vertical>",
  "sk": "PROFILE",
  "type": "VerticalStyleGuide",
  "vertical": "accountants",
  "tone": "professional, trustworthy, plain-English",
  "doPhrases": ["clear pricing", "fixed-fee accounting", "Making Tax Digital"],
  "dontPhrases": ["leverage synergies", "best-in-class", "world-class"],
  "antiPatterns": ["stock photo of handshakes", "generic team photo placeholder"],
  "palette": { "primary": "#0F4C81", "neutral": ["#0F172A", "#475569", "#F1F5F9"] },
  "version": 4,
  "createdAt": "...",
  "updatedAt": "...",
  "etag": "..."
}
```

### Email Tone Profile

```json
{
  "pk": "EMAIL_TONE#<vertical>",
  "sk": "PROFILE",
  "type": "EmailToneProfile",
  "vertical": "accountants",
  "subjectPatterns": [
    "Quick redesign preview for {{businessName}}",
    "Mocked up an alternate site for {{businessName}}"
  ],
  "openerPatterns": [
    "Hi {{firstName}},",
    "Hi {{firstName}} — saw {{businessName}} listed in {{location}}."
  ],
  "prohibitedPhrases": ["industry-leading", "best-in-class", "as you requested"],
  "signature": "Andrew\nAndrew Rea Associates\nhttps://techar.ch",
  "optOutLine": "Reply 'no thanks' and I won't follow up.",
  "version": 3,
  "createdAt": "...",
  "updatedAt": "...",
  "etag": "..."
}
```

### Business

```json
{
  "pk": "BUSINESS#<id>",
  "sk": "PROFILE",
  "type": "Business",
  "id": "<uuid>",
  "name": "Acme Accountants",
  "domain": "acmeaccountants.co.uk",
  "vertical": "accountants",
  "location": "Manchester, UK",
  "source": "companies-house",
  "sourceRefs": { "companiesHouseNumber": "01234567" },
  "discoveredAt": "...",
  "status": "qualified",
  "lastAuditId": "<uuid>",
  "lastSpecId": null,
  "lastWebsiteId": null,
  "createdAt": "...",
  "updatedAt": "...",
  "etag": "...",
  "gsi1pk": "BUSINESS#STATUS#<status>",
  "gsi1sk": "<priorityScore>#<businessId>",
  "gsi2pk": "BUSINESS#VERTICAL#<vertical>",
  "gsi2sk": "<discoveredAt>",
  "gsi3pk": "DOMAIN#<domain>",
  "gsi3sk": "PROFILE"
}
```

`status` values: `new` → `auditing` → `qualified` | `rejected` → `awaiting_review` → `approved` | `regenerate_requested` | `rejected_after_review` → `email_drafted` → `emailed` → `responded` → `converted`.

### Contact

```json
{
  "pk": "BUSINESS#<businessId>",
  "sk": "CONTACT#<id>",
  "type": "Contact",
  "id": "<uuid>",
  "name": "Jane Smith",
  "role": "Director",
  "email": "jane@acmeaccountants.co.uk",
  "confidence": 0.78,
  "source": "companies-house+website",
  "lastVerifiedAt": "...",
  "createdAt": "...",
  "updatedAt": "...",
  "etag": "..."
}
```

### Audit

```json
{
  "pk": "BUSINESS#<businessId>",
  "sk": "AUDIT#<id>",
  "type": "Audit",
  "id": "<uuid>",
  "technical": {
    "https": true,
    "viewport": true,
    "lighthouse": { "performance": 42, "accessibility": 71, "seo": 68 },
    "favicon": false,
    "contactDetected": true
  },
  "qualitative": {
    "modelId": "anthropic.claude-haiku-4-5",
    "summary": "Dated 2010s template, weak hero, no clear CTA above the fold.",
    "issues": [
      { "type": "conversion", "severity": "high", "description": "No primary CTA above the fold." },
      { "type": "design", "severity": "medium", "description": "Body type is Times New Roman 12pt." }
    ]
  },
  "score": 38,
  "worthRedesigning": true,
  "snapshotS3Key": "audits/<id>/homepage.html",
  "createdAt": "...",
  "etag": "...",
  "gsi1pk": "AUDIT#WORTH_REDESIGNING#<bool>",
  "gsi1sk": "<createdAt>"
}
```

### Qualification

```json
{
  "pk": "BUSINESS#<businessId>",
  "sk": "QUAL#<id>",
  "type": "Qualification",
  "qualified": true,
  "priorityScore": 0.82,
  "reasons": ["weak conversion structure", "owner email known", "vertical fit high"],
  "auditId": "<uuid>",
  "targetingProfileId": "<uuid>",
  "createdAt": "..."
}
```

### Spec

```json
{
  "pk": "BUSINESS#<businessId>",
  "sk": "SPEC#<id>",
  "type": "Spec",
  "id": "<uuid>",
  "version": 1,
  "status": "draft|approved|rejected",
  "content": { /* see 07-bedrock-prompts.md spec schema */ },
  "modelId": "anthropic.claude-sonnet-4-6",
  "promptId": "spec.v1",
  "approvedBy": null,
  "approvedAt": null,
  "createdAt": "...",
  "updatedAt": "...",
  "etag": "..."
}
```

### Website (the generated preview)

```json
{
  "pk": "BUSINESS#<businessId>",
  "sk": "WEBSITE#<id>",
  "type": "Website",
  "id": "<uuid>",
  "specId": "<uuid>",
  "previewUrl": "https://previews.example.com/sites/<websiteId>",
  "r2Prefix": "sites/<websiteId>/",
  "screenshots": {
    "desktop": "https://previews.example.com/screenshots/<websiteId>/desktop.png",
    "mobile":  "https://previews.example.com/screenshots/<websiteId>/mobile.png"
  },
  "lighthouseScore": 92,
  "passcodeHash": "<argon2id$...>",
  "passcodeCipher": "<base64 KMS ciphertext>",
  "passcodeRevealableUntil": <unix-7-days-future>,
  "passcodeRevokedAt": null,
  "status": "published|approved|rejected|regenerated",
  "approvedBy": null,
  "approvedAt": null,
  "createdAt": "...",
  "updatedAt": "...",
  "etag": "..."
}
```

**Passcode fields**:

- `passcodeHash` — argon2id hash of the passcode (salted with a per-env secret). Permanent — required by the Worker for validation. Never logged.
- `passcodeCipher` — KMS-encrypted cleartext. The publisher Lambda writes this; the email-draft Lambda (and the operator UI) read it via KMS decrypt to compose / display the email body. Erased after the cleanup Lambda runs 24h post `email.sent`, or unconditionally after `passcodeRevealableUntil` passes (default: 7 days after publish).
- `passcodeRevealableUntil` — Unix timestamp; once past, the cleartext is wiped even if no email was sent. Forces a regenerate-and-resend if the operator delays too long.
- `passcodeRevokedAt` — set when the operator manually revokes a passcode (e.g. after sharing the wrong recipient). Worker rejects validation; operator regenerates a new passcode (issues a new Website item).

The cleartext **never** appears in EventBridge payloads, structured logs, or X-Ray traces. The only places cleartext lives at rest are: `passcodeCipher` (KMS-protected), `EmailDraft.body` (already in DynamoDB), and the recipient's inbox. After `passcodeRevealableUntil` the operator cannot view the cleartext but the link still works for the recipient (the hash + Workers KV mapping persist).

### EmailDraft

```json
{
  "pk": "BUSINESS#<businessId>",
  "sk": "EMAIL_DRAFT#<id>",
  "type": "EmailDraft",
  "id": "<uuid>",
  "websiteId": "<uuid>",
  "contactId": "<uuid>",
  "subject": "Quick redesign preview for Acme Accountants",
  "body": "Hi Jane,\n\n...",
  "optOutLine": "Reply 'no thanks' and I won't follow up.",
  "wordCount": 137,
  "modelId": "anthropic.claude-haiku-4-5",
  "promptId": "email.v1",
  "status": "draft|approved|rejected|sent",
  "approvedBy": null,
  "approvedAt": null,
  "createdAt": "...",
  "updatedAt": "...",
  "etag": "..."
}
```

### EmailEvent

```json
{
  "pk": "BUSINESS#<businessId>",
  "sk": "EMAIL_EVENT#<timestamp>#<sesMessageId>",
  "type": "EmailEvent",
  "draftId": "<uuid>",
  "event": "sent|delivered|bounced|complained|opened|clicked|replied|unsubscribed",
  "sesMessageId": "...",
  "occurredAt": "..."
}
```

### Feedback

```json
{
  "pk": "FEEDBACK#<vertical>",
  "sk": "<createdAt>#<id>",
  "type": "Feedback",
  "id": "<uuid>",
  "subject": "audit|qualification|spec|website|email",
  "subjectId": "<uuid>",
  "businessId": "<uuid>",
  "actor": "<cognitoSub>",
  "action": "approve|reject|edit|regenerate",
  "originalPayload": { /* the artifact as it was generated */ },
  "editedPayload":   { /* the operator's overrides, if any */ },
  "notes": "Stop using 'industry-leading' in subjects.",
  "vertical": "accountants",
  "createdAt": "...",
  "gsi2pk": "FEEDBACK#VERTICAL#<vertical>",
  "gsi2sk": "<subject>#<createdAt>"
}
```

### Suppression

```json
{
  "pk": "SUPPRESSION#<emailLowercased>",
  "sk": "RECORD",
  "type": "Suppression",
  "reason": "bounce|complaint|manual|opt-out",
  "addedAt": "...",
  "expires_at": <unix-30-days-future>
}
```

### Idempotency record

```json
{
  "pk": "IDEMP#<eventId>",
  "sk": "RECORD",
  "type": "IdempotencyRecord",
  "consumer": "audit",
  "createdAt": "...",
  "ttl": <unix-24h-future>
}
```

### Cost ledger

```json
{
  "pk": "COST#<YYYY-MM-DD>",
  "sk": "STAGE#<stage>",
  "type": "CostRecord",
  "stage": "audit|spec|email|places|ses",
  "usd": 0.0421,
  "calls": 12
}
```

### Bedrock cache

```json
{
  "pk": "CACHE#BEDROCK#<promptId>",
  "sk": "<inputHash>",
  "type": "BedrockCacheEntry",
  "modelId": "...",
  "output": { /* parsed structured output */ },
  "tokensIn": 5230,
  "tokensOut": 612,
  "expires_at": <unix-30-days-future>
}
```

## Access patterns covered

| # | Pattern | Approach |
|---|---|---|
| 1 | Get business by id | `pk = BUSINESS#<id>, sk begins_with PROFILE` |
| 2 | List businesses by status (e.g., `awaiting_review`) ordered by priority | `gsi1` query |
| 3 | List businesses by vertical | `gsi2` query |
| 4 | Reject duplicate domain at discovery | `gsi3` lookup before `PutItem` |
| 5 | List audit / spec / website / email history for a business | `pk = BUSINESS#<id>, sk begins_with <prefix>` |
| 6 | Pipeline settings | `pk = SETTINGS#GLOBAL` |
| 7 | All targeting profiles enabled | `gsi1` query on `TARGET#ENABLED` |
| 8 | Style guide / tone profile by vertical | direct `pk` |
| 9 | Suppression check at send time | direct `pk` |
| 10 | Idempotency check at consumer entry | direct `pk` |
| 11 | Cost roll-up for a day | `pk = COST#<date>` query |
| 12 | Feedback aggregation per vertical for tuner | `gsi2` query |

## Optimistic concurrency

Every `Update`/`Put` on a non-event item must use `ConditionExpression: etag = :expected`. Handlers retry-and-merge on `ConditionalCheckFailedException`.

## TTL

The skeleton enables DynamoDB TTL on the `expires_at` attribute. Items that should auto-expire set `expires_at` to a future Unix timestamp. Used by `IdempotencyRecord`, `Suppression`, `BedrockCacheEntry`, and (in iter 11) lifecycle-aged events.

Items that must persist (`Business`, `Audit`, `Spec`, `Website`, `EmailDraft`, `EmailEvent`, `Feedback`, `CostRecord`) leave `expires_at` unset.

## Anti-patterns to avoid

- **Don't put hot counters on `Business`.** Use atomic counters under `pk=COUNTER#<scope>, sk=<bucket>` if you need them; otherwise compute via stream → analytics.
- **Don't query `Feedback` without GSI2.** It's intentionally not in the main partition for a business.
- **Don't store full HTML in DynamoDB.** Store in S3 (`audits/<id>/homepage.html`) and keep the key in the item.
