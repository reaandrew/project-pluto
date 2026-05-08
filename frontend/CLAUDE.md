# Claude Code instructions — `frontend/`

Vite 6 + React 19 + TypeScript. The **admin app** — operator review queue, settings, metrics, feedback log. Hosted on the skeleton's CloudFront + S3 (NOT Cloudflare Pages — those are for the *generated* business previews and live in `worker/` + R2). Per-env config via `runtime-config.js` (loaded at runtime from the deployed bucket prefix; lets one frontend bundle target multiple envs).

## Read first
- `.ralph/specs/01-architecture.md` § "Frontend (admin app)"
- `.ralph/specs/04-feedback-loops.md` — the three loops the UI must capture
- `.ralph/specs/08-admin-ui.md` — page contracts, action bar, access strip
- `.ralph/specs/10-quality-rules.md` § passcode display rules

## Conventions
- `vite.config.ts` `base: './'` is **load-bearing** — required for the per-PR preview path-prefix routing on the shared S3 bucket. Don't change it.
- Auth: Cognito Hosted UI redirect; session cookie translated by the BFF CFFn → `Authorization: Bearer <jwt>` to the API. Frontend reads `runtime-config.js` for the BFF/API URLs at boot.
- API calls: through `src/api.ts` only (one chokepoint for auth + error handling).
- Tests: `vitest` + Testing Library for components. One Playwright e2e per user-visible flow, run against the per-PR ephemeral env (NOT the shared dev env).
- Coverage: ≥85% on new code.

## Don't
- Import Node-only modules in route handlers / components — Vite will bundle them and break the build.
- Hardcode API URLs — read from `runtime-config.js` (`window.__RUNTIME_CONFIG__`).
- Render passcode cleartext beyond the access-strip "show/hide" component on `/queue/[id]`; never include in URLs, never in localStorage.
- Add a new feedback Capture handler that doesn't redact passcode to `{{PASSCODE}}` in `originalPayload` (`.ralph/specs/04-feedback-loops.md`).
