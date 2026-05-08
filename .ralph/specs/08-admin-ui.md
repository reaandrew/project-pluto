# 08 — Admin UI

The admin app is the cloud-skeleton's Vite 6 / React 19 / TypeScript SPA in `frontend/`. It is **NOT** a Next.js app and is **NOT** hosted on Cloudflare Pages. Hosting is the skeleton's CloudFront+S3 setup:

- Production: `https://<base-domain>/`
- Per-PR preview: `https://preview.<base-domain>/<env>/`

`frontend/` is built once per pipeline run; the per-env `runtime-config.js` is written next to `index.html` after the `aws s3 sync`. Never change `vite.config.ts`'s `base: './'` — it's load-bearing for the path-prefix preview (skeleton pitfall #17).

Auth: Cognito Hosted UI sets a session cookie. The BFF CloudFront's CFFn promotes the cookie into `Authorization: Bearer <jwt>` on the way to the API. The frontend calls the BFF (`bff.<base-domain>/` prod or `<env>.bff.<base-domain>/` preview) with `credentials: 'include'`; it never sees the JWT directly.

Routing: client-side via `react-router-dom`. No server-side routing.

## Routes

```
/login                         Cognito redirect
/                              Dashboard: pipeline status, today's queue summary, kill switch
/queue                         Review queue (the operator's primary surface)
/queue/[id]                    Single candidate: original site / preview / spec / contact / actions
/queue/[id]/email              Email draft review/edit
/backlog                       Qualified businesses awaiting a slot
/feedback                      Captured feedback log (filterable)
/tuners                        Pending tuner deltas (apply / dismiss)
/settings                      Pipeline settings (caps, budgets, kill switch)
/settings/targeting            Targeting Profiles CRUD
/settings/style/[vertical]     Vertical Style Guide editor
/settings/email-tone/[vertical] Email Tone Profile editor
/settings/email                SES domain status, suppression list, sender identity
/settings/discovery            Provider toggles + per-provider caps
/metrics                       Funnel + cost dashboard
/businesses                    All businesses (search, filter — secondary surface)
/businesses/[id]               Read-only business detail with full timeline
```

## Key surfaces

### Dashboard (`/`)
- Big switch: pipeline ON/OFF.
- Strip of stage toggles: Discovery / Audit / Preview / Outreach.
- "Today" panel: discovered, audited, qualified, awaiting review, drafts to review, sent, replied.
- Today's spend: Bedrock $X / $Y, Places $X / $Y, SES $X / $Y, with a progress bar against the cap.
- Last 5 events feed.
- Single button: "Pause for 24h".

### Review Queue (`/queue`) — the operator's primary surface

Card-grid layout, paginated by `priorityScore`.

```
┌─ Smile Dental — Manchester — dentist ──────────────────┐
│  ┌────────┐  Audit 38/100   Priority 0.86             │
│  │preview │  "Dated 2010s template, weak hero."        │
│  │  PNG   │  Contact: Jane Smith, jane@... (conf 0.78)│
│  └────────┘                                            │
│  Original: smiledental.co.uk    Preview: previews/...  │
│  [Open original] [Open preview] [View spec] [Diff]     │
│                                                        │
│  [✅ Approve]  [✏ Edit spec]  [↻ Regenerate]  [✗ Reject ▾]│
└────────────────────────────────────────────────────────┘
```

Filters: vertical, location, audit score range, contact-confidence range, source.

Reject menu requires a reason from the list in `04-feedback-loops.md` § Loop 1 plus optional notes. **All actions are recorded as `Feedback` events.**

### Single candidate (`/queue/[id]`)
Two-pane: original site (iframe with sandbox attributes) on the left, preview (iframe loading `previewUrl?p=<passcode>` so the operator's view bypasses the form) on the right. Below: the spec viewer with a structured editor, a contact panel, and the action bar.

Above the preview iframe, an **Access** strip:

```
Access:  previews.<base>/sites/abcd-1234   [Copy URL]
Code:    H7Q3-2KX9                          [Copy code]   [Show/Hide]   [Regenerate code]
Cleartext available for: 4d 12h
```

- **Copy URL** copies the bare URL (no passcode) — recipient enters the code in the Worker form.
- **Copy code** copies the cleartext.
- **Show/Hide** toggles cleartext visibility in the strip; default hidden.
- **Regenerate code** issues a new passcode (fires `preview.passcode.revoked` then `preview.passcode.issued`); the previous code stops working immediately. Used if the operator suspects a leak.
- **Cleartext available for** shows the time until `passcodeRevealableUntil`; once past, the cleartext is gone and the strip shows "Code wiped — regenerate to view".

Spec edits update the `Spec` item and bump its version; "Regenerate" (the spec/site action, distinct from "Regenerate code") re-runs the renderer (no Bedrock cost) and **issues a new passcode** automatically — old previews stop working. The action bar warns the operator if any email has already been sent against the current passcode (rare, but possible if they regenerate after sending).

### Email draft (`/queue/[id]/email`)
- Subject + body editor (monospaced; word counter).
- Live linter: highlights any prohibited phrase from the active `EmailToneProfile`; also highlights the passcode token in a distinct colour so the operator sees at a glance that the code is included exactly once.
- Static checks block "Approve & queue send" if: missing previewUrl, missing passcode, contains "password", contains a prohibited phrase, > 200 words.
- "Regenerate" calls Haiku again with notes. "Approve & queue send" enqueues the draft.

### Settings — pipeline (`/settings`)
- Master kill switch (binds to `pipelineEnabled`).
- Per-stage toggles.
- Sliders for daily caps with **live cost preview** computed from `05-capacity-and-cost.md` cost table.
- Budget caps in USD.
- Pause-for-24h, pause-until-I-unpause buttons.

### Settings — targeting (`/settings/targeting`)
List of `TargetingProfile`s. Click into one to edit:
- Vertical (free-text or pick from seeded list)
- Location (string)
- Include / exclude keywords
- Weights (sliders, must sum to ~1.0; UI normalizes)
- Enabled toggle
- "Run discovery now" button (manual trigger; respects caps)

### Settings — style (`/settings/style/[vertical]`)
Editor for `VerticalStyleGuide`:
- Tone (textarea)
- Do phrases (chip list)
- Don't phrases (chip list)
- Anti-patterns (chip list)
- Palette pickers (with WCAG AA contrast check shown live)
- "Preview against last 3 specs" — re-renders the spec previews using this style guide in dry-run.

### Settings — email tone (`/settings/email-tone/[vertical]`)
Editor for `EmailToneProfile`:
- Subject patterns (chip list, supports `{{businessName}}`, `{{firstName}}`, `{{vertical}}`)
- Opener patterns (chip list)
- Prohibited phrases (chip list)
- Signature block (textarea)
- Opt-out line (textarea)

### Settings — email infrastructure (`/settings/email`)
- SES domain verification status (DKIM/SPF/DMARC checks live).
- Sender identity (display name, reply address).
- Suppression list (search, manual add).
- Daily send cap.
- Domain warm-up plan (a multi-week schedule the system honors automatically).

### Tuners (`/tuners`)
Three sections: Targeting / Style / Email Tone. Each shows pending deltas for the relevant profile, with `[Apply]` `[Dismiss]` `[Diff]` buttons. Diff view shows red/green chip changes.

### Metrics (`/metrics`)
- Funnel: discovered → audited → qualified → preview → approved → emailed → opened → replied → converted, with per-stage drop-off.
- Reply-rate split by `EmailToneProfile.version`.
- Approve-rate split by `VerticalStyleGuide.version`.
- Daily/weekly spend breakdown.

## Components

Tailwind CSS + headless-ui (or shadcn-ui ported to Vite). All forms use `react-hook-form` + `zod` validators that mirror the API schemas. API client code lives in `frontend/src/api/` — extend the skeleton's existing `api.ts` rather than replacing it.

## Auth

Cognito Hosted UI. The skeleton's BFF CloudFront CFFn (`finance-cookie-to-auth` → renamed by `init.sh`) handles the cookie→Authorization Bearer translation. A small client-side guard checks for the cookie's presence on protected routes; if missing, it redirects to the Cognito Hosted UI login URL (read at runtime from `runtime-config.js`).

## Real-time updates

The dashboard and queue page poll `/api/queue/state` every 15 seconds. We don't use WebSockets — polling is simpler and cheap at this scale.

## Performance targets

- TTFB on Cloudflare edge: < 200ms.
- Queue page renders 20 candidates with thumbnails: < 1.5s on a cold cache.
- Spec editor open-to-typeable: < 500ms.

## Accessibility

WCAG AA. All actions keyboard-reachable; queue cards focusable; reject reason picker is a real `<select>`/`<combobox>`.

## Empty states

- `/queue` empty: "No candidates awaiting review. Discovery has produced N businesses today, M qualified for the backlog. [Open backlog]".
- `/tuners` empty: "No pending suggestions. Tuners run weekly on Sunday 02:00 UTC."
- `/feedback` empty: "Approve, edit, or reject your first candidate to start training the loops."

## Mobile

Responsive but optimized for desktop. The card grid collapses to single-column under 768px; all actions remain reachable.
