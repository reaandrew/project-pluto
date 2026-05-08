---
description: Summarize today's pipeline spend by stage. Optionally pass a YYYY-MM-DD as $ARGUMENTS to query a different day.
---

Summarize the pipeline's spend for the day: **$ARGUMENTS** (defaults to today, UTC).

Steps:

1. Determine the date: if `$ARGUMENTS` is non-empty, use it; otherwise compute today in UTC (YYYY-MM-DD).

2. Query the cost ledger from DynamoDB:

```bash
aws dynamodb query \
  --table-name <project>-items \
  --key-condition-expression "pk = :pk" \
  --expression-attribute-values '{":pk":{"S":"COST#<date>"}}' \
  --output json
```

(In the impl repo, you can use the `cost.GetSpendForDate()` helper from `lambdas/pkg/cost/` via `go run ./cmd/cost-summary` — but the raw DDB query also works.)

3. Pull the current `PipelineSettings.budgets` to compute remaining headroom:

```bash
aws dynamodb get-item \
  --table-name <project>-items \
  --key '{"pk":{"S":"SETTINGS#GLOBAL"},"sk":{"S":"PROFILE"}}' \
  --output json
```

4. Print a one-screen summary:

```
Cost ledger — <date>

Stage         Spent (USD)   Calls   Cap (USD)   Headroom   Status
audit         0.42          35      5.00         91%        ok
spec          1.18           7      5.00         76%        ok
email         0.02           3      1.00         98%        ok
places        0.51          30      2.00         75%        ok
ses           0.00           3      —            —          ok
─────────────────────────────────────────────────────────────────
total                                                       under budget

Stage flags:
  discoveryEnabled = true
  auditEnabled     = true
  previewEnabled   = true
  outreachEnabled  = false   ← OFF
```

If any stage is at >80% of cap, mark its row **WARN**. If any stage was paused-by-budget today, mark its row **PAUSED** and show the pause time.

5. If the operator has set the special arg `last7`, instead show a 7-day trend (one row per day, plus the 7-day total per stage).

Be terse. No prose; just the summary table and any warnings.

Notes for the calling Claude:
- This is a read-only command. Never write to the cost ledger or `PipelineSettings`.
- If the impl repo isn't checked out (running from the spec repo), explain that and ask for the table name + AWS profile.
