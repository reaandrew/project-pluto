# 03 — Event Contracts

All events are published to a custom EventBridge bus `pipeline-<env>` with `source = "agency.pipeline"` and `detail-type = "<event.name>"`. The bus is added by us in `terraform/eventbridge.tf` (the cloud-skeleton template ships only API Gateway + Lambda + DynamoDB + S3; we add events on top).

Per-env naming follows the skeleton's `local.env_suffix` pattern: production = `pipeline`, preview env = `pipeline-<branch>`. We never add the bus to `aws-setup/` (skeleton pitfall #10 — it's per-env, not a singleton).

## Envelope

Every event payload sits in `detail` and conforms to (Go shape — the source of truth lives in `lambdas/pkg/events/envelope.go`):

```go
// lambdas/pkg/events/envelope.go
type Envelope[T any] struct {
    EventID       string    `json:"eventId"`        // uuid v4 — used for idempotency
    EventName     string    `json:"eventName"`      // e.g. "business.found"
    EventVersion  int       `json:"eventVersion"`   // 1
    EmittedAt     time.Time `json:"emittedAt"`
    EmittedBy     string    `json:"emittedBy"`      // service name, e.g. "discovery"
    CorrelationID string    `json:"correlationId"`  // same value across the chain for one business journey
    CausationID   string    `json:"causationId,omitempty"` // eventId of the event that caused this one
    Detail        T         `json:"detail"`
}
```

The frontend mirrors this shape via TypeScript types generated from the Go source at build time (`scripts/gen-event-types.sh` — added by us alongside the bus).

`eventId`, `correlationId`, `causationId` are also surfaced as EventBridge `id` so rule filters and X-Ray traces match.

## Event catalogue

```
criteria.changed                   — operator updated a Targeting Profile
business.found                     — discovery returned a new business
contact.enriched                   — contact details attached to a business
website.audit.requested            — explicit audit kickoff (manual or scheduled)
website.audit.completed            — audit + qualitative review done
website.qualified                  — qualifier decided yes
website.rejected                   — qualifier decided no
spec.generation.requested          — qualified business needs a spec
spec.generated                     — spec produced and stored
spec.approved                      — operator approved a spec (or auto-approved)
spec.rejected                      — operator rejected
website.generation.requested       — start the generator
website.generated                  — HTML bundle produced
website.published                  — uploaded to R2 and reachable via Worker
website.approved                   — operator approved the preview
website.rejected_after_review      — operator rejected the preview
website.regenerate.requested       — operator asked for a regen with notes
outreach.email.requested           — start drafting an email
outreach.email_ready               — draft generated, awaiting approval
email.approved                     — operator approved the draft
email.rejected                     — operator rejected
email.sent                         — SES accepted
email.delivered                    — SES delivered
email.bounced                      — bounce notification
email.complained                   — complaint notification
email.opened                       — open pixel hit
email.clicked                      — preview-link click recorded
email.replied                      — reply detected
unsubscribe.received               — opt-out received
feedback.captured                  — operator override or note recorded
budget.cap.breached                — daily spend exceeded — pauses stage
queue.cap.reached                  — review queue at capacity — pauses preview gen
preview.passcode.issued            — Worker KV updated with new hash
preview.passcode.revoked           — operator revoked a passcode (Worker KV deleted)
preview.passcode.cleartext_wiped   — cleartext erased after sent or after window
```

## Detail shapes (the non-trivial ones)

### `business.found`
```json
{
  "businessId": "<uuid>",
  "domain": "acmeaccountants.co.uk",
  "name": "Acme Accountants",
  "vertical": "accountants",
  "location": "Manchester, UK",
  "source": "companies-house",
  "sourceRefs": { "companiesHouseNumber": "01234567" }
}
```

### `website.audit.completed`
```json
{
  "businessId": "<uuid>",
  "auditId": "<uuid>",
  "score": 38,
  "worthRedesigning": true,
  "priority": "high",
  "modelsUsed": ["pagespeed", "anthropic.claude-haiku-4-5"]
}
```

### `website.qualified`
```json
{
  "businessId": "<uuid>",
  "qualificationId": "<uuid>",
  "priorityScore": 0.82,
  "auditId": "<uuid>"
}
```

### `spec.generated`
```json
{
  "businessId": "<uuid>",
  "specId": "<uuid>",
  "tokensIn": 5421,
  "tokensOut": 1923,
  "modelId": "anthropic.claude-sonnet-4-6"
}
```

### `website.published`
```json
{
  "businessId": "<uuid>",
  "websiteId": "<uuid>",
  "previewUrl": "https://previews.example.com/sites/<websiteId>",
  "lighthouseScore": 92,
  "passcodeIssued": true,
  "passcodeRevealableUntil": "2026-05-15T11:23:00Z"
}
```

**Never** include the passcode cleartext in any event payload. Consumers that need it (email-draft) read `Website.passcodeCipher` and KMS-decrypt.

### `outreach.email_ready`
```json
{
  "businessId": "<uuid>",
  "draftId": "<uuid>",
  "websiteId": "<uuid>",
  "contactId": "<uuid>",
  "wordCount": 137
}
```

### `feedback.captured`
```json
{
  "feedbackId": "<uuid>",
  "businessId": "<uuid>",
  "subject": "spec",
  "subjectId": "<uuid>",
  "actor": "<cognitoSub>",
  "action": "edit",
  "vertical": "accountants",
  "summary": "removed 'industry-leading'; added 'fixed-fee accounting'"
}
```

### `budget.cap.breached`
```json
{
  "stage": "spec",
  "date": "2026-05-08",
  "spentUsd": 5.12,
  "capUsd": 5.0,
  "actionTaken": "previewEnabled=false"
}
```

## Routing rules (EventBridge)

| Source rule | Target |
|---|---|
| `business.found` | `audit` Lambda (via SQS) |
| `website.audit.completed` AND `detail.worthRedesigning = true` | `qualifier` Lambda |
| `website.qualified` | `spec-generator` Lambda |
| `spec.approved` | `generator` Lambda |
| `website.generated` | `publisher` Lambda |
| `website.published` | screenshot Lambda |
| `website.approved` | `email-draft` Lambda |
| `email.approved` | `sender` Lambda |
| `email.bounced` OR `email.complained` | `suppression-updater` Lambda |
| `feedback.captured` | (a) DynamoDB write via direct call; (b) emitted again so weekly tuners can pull from EventBridge archive |

All consumers go via SQS for retry + DLQ; rules use `RetryPolicy { MaximumRetryAttempts: 3, MaximumEventAgeInSeconds: 3600 }`.

## Schemas

The Go types in `lambdas/pkg/events/details.go` are the source of truth for event detail shapes. EventBridge Schema Registry registers each shape under `agency.events.<event-name>`. Codegen runs in CI to produce `frontend/src/api/events.ts` (TypeScript) from the Go source so the admin app cannot drift from the schema.

## Replay & archive

- Archive on the bus retains 30 days in `dev`/`staging`, 90 days in `prod`.
- Tuners (iteration 9) replay `feedback.captured` from the archive into a tuner queue weekly.

## Versioning

`eventVersion` is currently `1` for all events. Adding a field is non-breaking. Renaming or removing requires a new event name (`<event-name>.v2`) until consumers migrate.
