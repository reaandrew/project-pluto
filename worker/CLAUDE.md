# Claude Code instructions — `worker/`

Cloudflare Worker (TypeScript) — passcode-gated preview server. Serves the generated business previews from R2, validates a passcode on a signed cookie or `?p=…` query param, rate-limits per IP. Provisioned in iteration 0.D and built in iteration 5.

## Read first
- `.ralph/specs/01-architecture.md` § "Generated business previews" — R2 + Worker + KV + Rate Limiting
- `.ralph/specs/02-data-model.md` — `Website.passcodeCipher`, `passcodeRevealableUntil`
- `.ralph/specs/10-quality-rules.md` — the passcode-cleartext rule (non-negotiable)
- `.ralph/specs/09-iterations.md` § Iteration 0.D + Iteration 5

## Routes
- `GET /sites/{websiteId}` and asset paths under it — cookie OR `?p=<passcode>` validation; serve passcode form on miss
- `POST /sites/{websiteId}` — passcode form submit
- `GET /screenshots/{websiteId}/{size}.png` — same passcode rule
- `GET /healthz` — 200 always

## Conventions
- **Edge runtime only** — no Node APIs (`fs`, `crypto.createHash`, `process.env` outside `env`). Use Web Crypto, Workers KV, R2 bindings.
- **Constant-time argon2id compare** via WASM (smuggled in as a binding asset; do NOT roll your own).
- **HMAC-SHA256 signed cookie** scoped to `Path=/sites/<websiteId>/`, `Secure; HttpOnly; SameSite=Lax`. TTL 24h. Sign with `PASSCODE_SALT` secret bound to the worker.
- **Never log the passcode cleartext.** Hash it for cache-key purposes; redact in any error message.
- **Tests**: `vitest` + `@cloudflare/vitest-pool-workers` (Miniflare). Unit-test the validator + cookie signer. One e2e per route on the deployed worker.

## Don't
- Use `crypto.subtle` for argon2id — it's not in the Web Crypto algorithms list. Use the WASM binding.
- Read passcode from a header that the upstream CDN might log.
- Bypass the rate limiter on the `?p=…` path; this is the brute-force surface.
