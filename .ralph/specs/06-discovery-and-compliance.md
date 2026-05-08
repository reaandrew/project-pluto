# 06 — Discovery Sources & Compliance

## Discovery providers

Provider interface in `services/discovery/providers/`:

```ts
export interface DiscoveryProvider {
  id: "companies-house" | "google-places" | "csv" | "yell" | "bing-places";
  enabled: boolean;
  capPerDay: number;
  costPerCall: number; // USD
  query(profile: TargetingProfile, cursor?: string): AsyncIterable<DiscoveredBusiness>;
}

export type DiscoveredBusiness = {
  name: string;
  domain: string | null;     // may be null for Companies House — resolve later
  location?: string;
  vertical?: string;
  source: string;
  sourceRefs: Record<string, string>;
  confidence: number;        // 0..1
};
```

### MVP providers (iteration 1)

| Provider | Cost | Notes |
|---|---|---|
| **CompaniesHouseProvider** | Free (rate-limited 600/5min) | Primary UK provider. Use the Search Companies endpoint plus officer lookup. Domains are not in CH; resolve via website inferred from company name + vertical query (Google CSE) only after qualification pre-check. |
| **GooglePlacesProvider** | ~$17/1k Place Search | Capped via daily-spend cap. Use New Place Search + Place Details for website + opening hours. Better domain coverage than CH. |
| **CsvProvider** | Free | Operator uploads CSV; parses to `DiscoveredBusiness`. Used for backfills, partner lists, manual research. |

### Later providers (iteration 11+)

- BingPlacesProvider
- YellProvider — see compliance section below
- LinkedInProvider — only via official Sales Navigator API, never scraping

## Provider abstraction rules

- One provider per file; no inline `fetch` to provider URLs from the discovery Lambda.
- Each provider declares its `capPerDay`; the Lambda enforces it via a per-provider counter alongside the global discovery cap.
- Each provider has its own circuit breaker: 3 consecutive 5xx responses → mark provider unhealthy for 15 min.
- Each provider response is cached by `(providerId, queryHash)` for the duration the provider's data is reasonably fresh (CH: 24h; Places: 7d).

## Robots.txt and rate limiting

The system never crawls anywhere it isn't allowed. Implementation:

- `services/lib/robots.ts` — fetches and caches `robots.txt` per host for 24h.
- Every outbound HTTP fetch (audit homepage fetch, contact-page fetch, screenshot of original site) goes through `politeFetch(url)` which:
  1. Reads cached robots; if disallowed for `*` or our user agent, refuses.
  2. Honors `Crawl-delay`.
  3. Sets `User-Agent: WebsiteAgencyBot/1.0 (+https://outreach.example.com/bot)`.
  4. Throttles to 1 request per host per 5 seconds globally (token bucket in DynamoDB).
  5. On 429/5xx: exponential backoff with jitter; 3 retries then give up.
- We never run more than one concurrent fetch per host across the fleet.

## GDPR, PECR, CAN-SPAM compliance

The pipeline operates from the UK, may reach contacts in UK / EU / US. We must comply with:

- **UK GDPR + PECR** for UK contacts.
- **EU GDPR + ePrivacy** for EU contacts.
- **CAN-SPAM** for US contacts.

### Standing rules

1. **Lawful basis.** B2B outreach to a publicly listed business email of a director relating to their professional role can rely on Legitimate Interest (UK GDPR Art. 6(1)(f)). Personal emails (sole-trader Gmails identified by enrichment as personal) get a stricter bar — exclude unless we have `corporate-form: SOLE_TRADER` consent documented.
2. **Identify ourselves.** Every email includes our company name, postal address, and a working reply address.
3. **Make opt-out trivial.** Every email includes:
   - A `List-Unsubscribe` header (RFC 8058 one-click).
   - A free-text "reply 'no thanks' and I won't follow up" line.
   - A working unsubscribe URL.
4. **Honor opt-out within 10 days** — practically, we suppress immediately on receipt.
5. **No deceptive subject lines or sender identity.** From: header is the real sender's real name. Subject must accurately describe content.
6. **No statement that we built or published their site.** We say "private preview" — see `10-quality-rules.md`.
7. **Keep a record of decisions.** The `Feedback` log + `EmailEvent` log together provide auditability.
8. **DPA-relevant data** (email addresses, names, IP addresses if any) gets a 12-month retention by default; right-to-erasure honored via `/admin/erasure`.

### Yell.co.uk specifically

Yell's robots.txt is permissive but has many disallowed paths and the platform applies bot protection. Treat Yell as:

- A throttled secondary source, never primary.
- Used for enrichment of single business pages discovered elsewhere — never as a directory crawler.
- Capped at < 100 requests/day across the fleet.
- Skipped entirely if `User-Agent` is challenged.

Better: use `site:yell.com` Google queries via the official Custom Search API to find specific listings, then visit only those.

## Suppression list

A single DynamoDB table partition (`pk=SUPPRESSION#<emailLowercased>`) holds:
- All explicit opt-outs (`reason=opt-out`).
- All bounces (`reason=bounce`) — auto-added on SES bounce notification.
- All complaints (`reason=complaint`).
- Manually added (`reason=manual`).

The `sender` Lambda **must** check suppression on every send; this is enforced by a unit test that fails if the handler entry doesn't call `assertNotSuppressed(email)` before the SES API call.

## Domain warm-up

For SES outreach we use a dedicated subdomain (`outreach.<domain>`) that is **not** the corporate domain. Warm-up plan in `08-admin-ui.md` § Outreach settings.

## Decision: enrichment providers (Hunter, Apollo, Clay)

Not in MVP. They cost money and bring lawful-basis complexity — third-party enrichment of personal emails is harder to justify under GDPR than fetching a director email from a company website. Add only after MVP proves out, and only with a documented DPIA.
