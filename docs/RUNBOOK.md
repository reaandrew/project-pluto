# Operator Runbook

How to log in and run the website-redesign outreach platform day to day.
This is the introduction for a new operator. It assumes the platform is
already deployed (see `docs/BOOTSTRAP.md`) — it does **not** cover
infrastructure setup.

---

## 1. What the platform does

It runs an outbound pipeline that, mostly on its own:

1. **Discovers** small UK businesses (hourly).
2. **Audits** their existing website and **qualifies** the good leads.
3. **Generates** a redesign **spec**, then a **preview site**, publishes it
   behind a passcode, and takes **screenshots**.
4. Puts each candidate in your **review queue**.
5. After you approve, **drafts and sends** a cold-outreach email via SES.
6. **Tracks** bounces, complaints, opens, and **replies** (auto-classified).
7. **Cleans up** the passcode 24h after sending.
8. Turns your approve/reject/edit decisions into weekly **tuner proposals**
   you can apply to improve targeting, site style, and email tone.
9. Rolls up **funnel + cost + per-vertical** metrics for you to watch.

Your job is the human-judgement parts: **review the queue, handle
ambiguous replies, review tuner proposals, watch the metrics**, and pull
the kill switch if something looks wrong.

---

## 2. Before you can use it (one-time, done by an admin)

These are HUMAN-ONLY steps. If outreach isn't sending, this is almost
always why:

- **AWS + Cloudflare bootstrap** — `docs/BOOTSTRAP.md`.
- **SES out of sandbox + inbound receive enabled** — `docs/SES.md`.
  Until this is done the pipeline runs but **no email is delivered and no
  replies are ingested**.
- **At least one Targeting profile created and enabled** (via
  *Settings → Targeting*), otherwise discovery has nothing to look for.
- **Your operator account exists** (next section).

---

## 3. Getting an account

The operator user pool is **invite-only — there is no self-signup**. An
administrator with AWS access creates your account and adds you to the
`operator` group (every API call is rejected with *403 operator group
required* if you are not in that group):

```bash
# Admin runs this once, with the project's aws-vault profile:
aws-vault exec personal_iphone -- aws cognito-idp admin-create-user \
  --user-pool-id <cognito_user_pool_id> \
  --username you@example.com \
  --user-attributes Name=email,Value=you@example.com Name=email_verified,Value=true

aws-vault exec personal_iphone -- aws cognito-idp admin-add-user-to-group \
  --user-pool-id <cognito_user_pool_id> \
  --username you@example.com \
  --group-name operator
```

(`<cognito_user_pool_id>` is the `cognito_user_pool_id` Terraform output.)
You'll get a temporary password by email and set a real one on first
login.

---

## 4. Logging in

1. Open the admin app: **https://agency.techar.ch/**
2. You're not signed in, so you're bounced to the Cognito Hosted UI.
3. Enter your email + password (set a new password on first login).
4. You land back on the **Dashboard**. A session cookie keeps you signed
   in; if it expires you're simply redirected to sign in again.

If you see *"operator group required"* anywhere, your account exists but
isn't in the `operator` group — ask the admin to run the
`admin-add-user-to-group` command above.

---

## 5. The screens (in the order you'll use them)

The nav bar across the top has every screen. The daily workflow flows
left to right.

### Dashboard (`/`)
At-a-glance landing page.

### Queue (`/queue`) — your main job
Businesses **awaiting review**, highest-priority first. For each:

- Open it to see the audit findings, the generated **spec**, and the
  **preview site** (with the access passcode revealed for you only).
- **Approve** → the email is drafted and (after you approve the draft)
  sent. **Reject** (with a reason) → it's dropped. **Regenerate** → ask
  for a fresh spec/site.
- The email draft itself gets a second review (**Edit / Approve /
  Reject**) on the candidate's email screen before anything is sent.

Every decision you make here is captured as feedback that trains the
weekly tuners.

> The "X reviewed of N today" line uses your daily review-queue cap from
> *Settings*. Don't burn through more than you can sustain — quality of
> review is what the tuners learn from.

### Replies (`/replies`) — the reply inbox
When a prospect replies, it's auto-classified:

- **unsubscribe** (high confidence) → automatically suppressed + the
  business is closed out. You don't need to act.
- **positive interest** → the business is marked *responded*. Follow up
  out-of-band.
- **unknown / low confidence** → it lands **here** for you to read and
  **reclassify** (Unsubscribe / Positive interest / Unknown). Your
  reclassification re-applies the right action and is logged.

Reply bodies are shown to you for triage but are never put in logs or
events.

### Feedback (`/feedback`) — the audit log
Every Approve / Edit / Reject / Apply across Spec / Website / Email /
Profiles, filterable by vertical, stage, and date. This is the trail of
who-did-what; it does not show the email bodies (those are redacted).

### Tuners (`/tuners`) — the learning loop
Once a week the tuners read your recent feedback and propose
**deltas** to the targeting / site-style / email-tone profiles. Each
proposal shows the rationale and the exact change.

- **Apply** → the live profile is updated, its version is bumped, an
  audit row is written, and downstream caches invalidate. Nothing is
  auto-applied — a delta only takes effect when **you** apply it.
- **Dismiss** → recorded and discarded.

Use **Metrics** to decide: did reply rate move after you applied the
last delta?

### Metrics (`/metrics`) — funnel, cost, comparison
- **Discoveries** — recent leads + a 7-day count, plus a *Run discovery
  now* button.
- **Funnel** — counts at every stage from *new* to *converted*.
- **Cost** — Bedrock + SES + Places spend over a date window, by stage.
- **Vertical comparison** — reply rate / conversion rate per vertical,
  sorted by reply rate, with the style/tone profile version in effect so
  you can correlate a tuner change with a rate move.

### Settings (`/settings`, `/settings/targeting`)
Pipeline controls: per-stage **kill switches** (pause discovery / audit /
preview / outreach), **daily budget caps** (Bedrock / SES / Places — a
stage auto-pauses if its cap is hit), the review-queue cap, the sender
identity, and the **Targeting** profiles (keywords + priority weights)
that drive discovery.

---

## 6. The daily loop

1. **Metrics** — glance at the funnel and yesterday's cost. Anything
   stuck or a cost spike?
2. **Queue** — work the review queue (approve good redesigns, reject the
   rest, approve/edit the email drafts). This is the bulk of the work.
3. **Replies** — clear the reply inbox; reclassify the *unknown* ones.
4. **Tuners** (weekly, after the Sunday run) — review proposals; apply
   the ones that make sense; check Metrics next week for the effect.

---

## 7. If something looks wrong

- **No emails going out?** First suspect: SES still in sandbox / inbound
  not enabled (`docs/SES.md`), or the **outreach kill switch** is off in
  *Settings*, or the **daily SES/Bedrock budget cap** was hit (the stage
  auto-pauses and shows the reason; caps reset at 00:05 UTC).
- **Want to stop everything immediately?** *Settings* → turn off the
  relevant stage kill switch. Inbound compliance (suppression, reply
  handling, passcode cleanup) keeps running by design even when outreach
  is paused.
- **A bad batch went out?** Suppression and unsubscribe are automatic;
  for a specific recipient, reclassify their reply as *unsubscribe* in
  *Replies* (adds them to the SES suppression list).
- **A business never got a preview / passcode looks wiped?** The
  cleartext passcode is deliberately erased 24h after send (or 7 days
  after publish). The recipient's link still works — only your ability to
  re-view the code goes away. **Regenerate** the passcode from the
  candidate screen to view/resend.

---

## 8. Hard rules the platform enforces for you

You don't have to police these — they're built in — but know they exist:
no fake testimonials/awards/prices in generated content; the preview is
never described as published; every email has a working one-click
unsubscribe and an honest sender; the passcode cleartext is never logged
or emailed in plain pipeline events; suppression fails closed (if SES
status is unknown, it does **not** send).
