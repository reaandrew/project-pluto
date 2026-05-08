# 05 — Capacity Controls & Cost

## Goals

1. The pipeline never overloads the operator's review capacity.
2. The pipeline never silently overspends.
3. Every paid call records its cost; daily caps trip kill switches automatically.
4. The operator has one button that pauses everything.

## The Pipeline Settings record (single source of truth)

```json
{
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
  }
}
```

Defaults shipped at provisioning time. UI at `/settings`. Read on every consumer entry (cached 60 seconds in Lambda warm container).

## How each control behaves

### `pipelineEnabled = false`
Every consumer Lambda checks `pipelineEnabled` first. If false, returns success without acting and emits `pipeline.skipped_killed` metric. The discovery scheduler itself also checks this and exits early.

### Per-stage flags
Same pattern, but only the named stage stops. So you can pause `outreachEnabled` while leaving discovery and audit running to fill the queue.

### Daily caps
A counter in DynamoDB at `pk=CAP#<YYYY-MM-DD>, sk=STAGE#<stage>, count=N` is `UpdateItem` `ADD count :one` before each stage runs. If the new value exceeds the cap, the consumer:
1. Decrements (best-effort — counter slop is ok),
2. Records `pipeline.skipped_capped` metric,
3. For preview/outreach: re-enqueues the work with `delaySeconds=86400` so it picks up tomorrow.

For discovery: just stops.

### Review queue cap
When the operator reaches `maxReviewQueueSize` items in `awaiting_review`, the **preview generator** auto-pauses for new items (existing in-flight finishes). The qualifier still runs and parks new qualified businesses in the **backlog** (`status = qualified, awaitingPromotion = true`).

When an item leaves `awaiting_review` (approved/rejected), a `queue.slot.freed` event triggers a `backlog-promoter` Lambda that picks the highest-priority backlog entry and kicks off preview generation.

### Budget caps
Each stage's invocation wrapper:
```ts
async function withCostCap(stage, estimateUsd, fn) {
  const today = todayUtc();
  const spent = await getSpend(today, stage);
  const cap = await getCap(today, stage);
  if (spent + estimateUsd > cap) {
    await pauseStage(stage); // sets stages.<stage>Enabled = false, emits budget.cap.breached
    throw new BudgetCapExceeded(stage);
  }
  const result = await fn();
  await recordSpend(stage, result.actualUsd);
  return result;
}
```

`pauseStage` flips the relevant `stages.*Enabled` to `false`. A separate "rollover" Lambda fires at 00:05 UTC daily, resetting all counters and re-enabling stages whose pause reason was `budget`.

## Priority scoring

Pure function. No I/O. Tested against golden cases.

```ts
function priorityScore(input: {
  audit: Audit;
  business: Business;
  contact: Contact | null;
  targetingProfile: TargetingProfile;
}): number {
  const w = input.targetingProfile.weights;
  const websiteNeed = clamp(0, 1, (100 - input.audit.score) / 100); // worse audit → higher need
  const verticalFit = input.business.vertical === input.targetingProfile.vertical ? 1 : 0.5;
  const businessSize = sizeProxy(input.business); // 0..1
  const contactConf = input.contact?.confidence ?? 0;
  const ageProxy = websiteAgeProxy(input.audit); // 0..1, older → higher

  return (
    w.auditScore * websiteNeed +
    w.verticalFit * verticalFit +
    w.businessSize * businessSize +
    w.contactConfidence * contactConf +
    w.websiteAge * ageProxy
  );
}
```

Returns `[0, 1]`. Stored on the `Qualification` and used to sort the review queue + backlog.

## Cost model (target spend at moderate caps)

Caps used for the moderate-budget profile (~£50–200/month):
- Discoveries: 100/day
- Audits: 50/day
- Previews: 10/day
- Emails: 5/day

| Stage | Cost per call | Calls/day | $/day | $/month |
|---|---|---|---|---|
| Companies House | free | 100 | 0 | 0 |
| Google Places | $0.017 | 30 | $0.51 | $15 |
| PageSpeed Insights | free | 50 | 0 | 0 |
| Audit Bedrock (Haiku 4.5) | $0.012 | 50 | $0.60 | $18 |
| Spec Bedrock (Sonnet 4.6) | $0.075 | 10 | $0.75 | $22 |
| Email Bedrock (Haiku 4.5) | $0.005 | 5 | $0.025 | $0.75 |
| Cloudflare Browser Rendering | $0.0005 | 10 | $0.005 | $0.15 |
| SES | $0.0001 | 5 | negligible | <$0.01 |
| Lambda + DynamoDB on-demand | — | — | — | <$5 |
| R2 storage + Worker + KV + Rate-Limit | — | — | — | <$2 |
| KMS (passcode encrypt + decrypt) | $0.03/10k | <100/day | negligible | <$0.10 |
| **Total** | | | **~$1.90/day** | **~£50/month** |

At 30 previews/day and Sonnet for headlines, total tracks closer to £100–£150/month. The £200 cap is the alarm threshold; the budget cap stops it before then.

## Alarms

- CloudWatch metric alarm: `dailyBedrockUsd > 0.8 * cap` for 30 minutes → SNS notification.
- `dailyPlacesUsd > 0.8 * cap` → notification.
- Anomaly alarm on `lambda:Errors` per service.
- DLQ depth > 0 for 10 min on any consumer.
- SES bounce rate > 5% over 24h → outreach auto-paused.

## Operator-facing capacity UI

`/settings` exposes:
- Master kill switch: `pipelineEnabled`.
- Per-stage toggles: discovery, audit, preview, outreach.
- Sliders for caps (with the cost estimate updating live: "10 previews/day ≈ £22/month at current Sonnet rates").
- Budget caps in USD.
- "Pause for 24h" / "Pause until I unpause" buttons.

The slider preview is computed client-side from a static cost table that mirrors this document. Update both when models change price.

## Ralph rule for new producers

Any new Lambda that calls a paid API or Bedrock **must**:
1. Read pipelineEnabled + stage flag at handler entry.
2. Wrap the paid call in `withCostCap(stage, estimate, fn)`.
3. Cache outputs in DynamoDB (`CACHE#BEDROCK#<promptId>` / `CACHE#PLACES#<query>` / etc.) before invoking, with 30-day TTL.
4. Emit `pipeline.<stage>.{succeeded,failed,skipped_capped,skipped_killed}` metrics.
5. Be idempotent on `eventId`.

Failure to do these is a hard PR-block in CI (we add a static check that imports of `BedrockClient` outside `withCostCap` fail the build).
