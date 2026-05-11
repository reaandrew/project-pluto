# Claude Code instructions — `lambdas/`

Go 1.24, AWS Lambda, `provided.al2023` x86_64. Each service is `lambdas/<name>/` with `main.go` + `main_test.go` + a placeholder `bootstrap` (skeleton pitfall #3 — committed so `terraform plan` succeeds on a fresh checkout). Shared code in `lambdas/pkg/`. Module: `github.com/reaandrew/ai-website-agency/lambdas`.

## Read first
- `.ralph/specs/02-data-model.md` — DynamoDB key shape, item types
- `.ralph/specs/03-events.md` — event envelope, custom-bus contract
- `.ralph/specs/05-capacity-and-cost.md` — caps, cost ledger
- `.ralph/specs/07-bedrock-prompts.md` — prompt versions + tool-use schemas
- `.ralph/specs/stdlib/idempotency-patterns.md`
- `.ralph/specs/stdlib/json-output-conventions.md`
- `.ralph/specs/10-quality-rules.md` — non-negotiables

## Five rules at every handler entry (non-negotiable)
1. **Kill switch** — `pkg/killswitch.Allowed(ctx, "<stage>")` first; bail with success on disabled.
2. **Idempotency** — wrap pure work in `pkg/idempotency.WithIdempotency[T](ctx, key, fn)` keyed on the producer-supplied event `id` or a deterministic stable hash.
3. **Cost cap** — for any paid call (Bedrock, Google Places, screenshots), wrap in `pkg/cost.WithCostCap(ctx, stage, fn)`; record on success.
4. **Polite fetch** — outbound HTTP to **scrape-style targets** (the business's own homepage, contact page, screenshot of original site — anywhere robots.txt applies) goes through `pkg/politefetch.Client` (robots.txt + 1 req/s + ETag cache). Authenticated official APIs that publish their own rate-limit contract (Companies House, Google Places, Bedrock, SES, KMS, Cloudflare) use plain `http.Client` with auth headers — robots.txt doesn't apply and politefetch's ETag cache is wrong for JSON APIs. Both paths inject an `HTTPDoer` interface for tests so neither hits the network in unit tests.
5. **Cache TTL** — Bedrock outputs cached in DynamoDB with `expires_at` (TTL-enabled); cache key includes prompt version.

## Conventions
- Errors: wrap with `%w`, never bare `errors.New("…")` for caller-visible failures.
- No panics in handlers; convert to `error` and let the runtime convert to a 500.
- Logs: structured (`slog`); never log secrets, never log passcode cleartext (see `.ralph/specs/10-quality-rules.md`).
- Tests: unit-test the pure function directly; handler-level tests use `aws-sdk-go-v2/...` test doubles. ≥85% coverage on new code.

## Don't
- Hardcode region, account, or domain — read from env / SSM via `pkg/config`.
- Add a new lambda without `pkg/killswitch` + the four other rules.
- Touch `bootstrap` in version control beyond the placeholder; CI overwrites it with the real binary.
