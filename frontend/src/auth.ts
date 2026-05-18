// PKCE helpers + cookie write for the SPA-side Cognito Hosted UI flow.
//
// Flow:
//   1. Login.tsx calls beginPkceFlow() — generates a verifier, stashes
//      it in sessionStorage, and returns the Cognito Hosted UI URL with
//      the matching code_challenge appended.
//   2. Cognito redirects back to /oauth/callback?code=… on the same SPA.
//   3. Callback.tsx calls completePkceFlow(code) — POSTs to the
//      /oauth2/token endpoint with the stored verifier, gets an
//      id_token, writes it as the `auth_token` cookie.
//
// Cookie posture: non-HttpOnly because the SPA needs JS-level write
// access here. The XSS-blast-radius mitigation lives at the BFF
// boundary: the cookie-to-auth CloudFront Function converts the
// cookie into an Authorization header, and API Gateway validates the
// JWT signature/audience/expiry on every request. A future iter can
// move the exchange to a backend Lambda + HttpOnly cookie if the
// extra defence-in-depth is worth the infra.

const VERIFIER_KEY = 'pkce_verifier';

// beginPkceFlow returns the Cognito Hosted UI login URL with PKCE
// query params appended. The verifier is squirrelled into
// sessionStorage so completePkceFlow can recover it.
export async function beginPkceFlow(loginUrl: string): Promise<string> {
  const verifier = generateCodeVerifier();
  sessionStorage.setItem(VERIFIER_KEY, verifier);
  const challenge = await codeChallenge(verifier);
  const url = new URL(loginUrl);
  url.searchParams.set('code_challenge', challenge);
  url.searchParams.set('code_challenge_method', 'S256');
  return url.toString();
}

// completePkceFlow turns an authorization code into an id_token via
// the Cognito /oauth2/token endpoint, then writes that id_token as
// the `auth_token` cookie. Throws when the verifier is missing
// (someone hit /oauth/callback directly without going through
// /login first) or when Cognito rejects the exchange.
export async function completePkceFlow(
  code: string,
  authOrigin: string,
  clientId: string,
  redirectUri: string,
  bffBaseUrl: string
): Promise<void> {
  const verifier = sessionStorage.getItem(VERIFIER_KEY);
  if (!verifier) {
    throw new Error('Missing PKCE verifier — restart the sign-in flow at /login.');
  }
  const body = new URLSearchParams({
    grant_type: 'authorization_code',
    client_id: clientId,
    code,
    code_verifier: verifier,
    redirect_uri: redirectUri,
  });
  const res = await fetch(`https://${authOrigin}/oauth2/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: body.toString(),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`Token exchange failed (HTTP ${res.status}): ${text}`);
  }
  const tokens = (await res.json()) as { id_token: string };
  if (!tokens.id_token) {
    throw new Error('Token exchange response is missing id_token.');
  }
  setAuthCookie(tokens.id_token, bffBaseUrl);
  sessionStorage.removeItem(VERIFIER_KEY);
}

// cookieScopeDomain returns the cookie `Domain` so a cookie written by
// the SPA host is ALSO sent to the BFF host (a sibling subdomain). It
// is the longest shared dotted suffix of the two hostnames — e.g.
// SPA `agency.techar.ch` + BFF `bff.agency.techar.ch` → `agency.techar.ch`;
// preview `preview.agency.techar.ch` + `x.bff.agency.techar.ch` →
// `agency.techar.ch`. Returns '' (a host-only cookie) when there is no
// usable shared parent — single-label hosts like `localhost`, an IP, or
// only a bare public suffix in common. WITHOUT this the auth_token
// cookie is host-only on the SPA domain and never reaches the BFF, so
// every authenticated route 401s.
// Two-level public suffixes where the registrable domain is the last
// THREE labels (e.g. `foo.co.uk`). Not the full PSL — just the ones we
// could plausibly move to — so a `.co.uk`-style move can't silently
// scope the cookie to a bare public suffix. The current production
// domain (`agency.techar.ch`) is a single-label TLD and unaffected.
const TWO_LEVEL_SUFFIXES = new Set([
  'co.uk',
  'org.uk',
  'gov.uk',
  'ac.uk',
  'com.au',
  'net.au',
  'org.au',
  'co.nz',
  'co.za',
  'com.br',
]);

export function cookieScopeDomain(spaHost: string, bffHost: string): string {
  if (!spaHost || !bffHost || spaHost === bffHost) return '';
  const a = spaHost.split('.');
  const b = bffHost.split('.');
  const shared: string[] = [];
  for (let i = a.length - 1, j = b.length - 1; i >= 0 && j >= 0 && a[i] === b[j]; i--, j--) {
    shared.unshift(a[i]);
  }
  // Minimum registrable-looking parent: ≥2 labels normally, but ≥3 when
  // the tail is a known two-level public suffix (so we never emit a
  // bare `co.uk`-style Domain that broadcasts the cookie site-wide).
  const minLabels = TWO_LEVEL_SUFFIXES.has(shared.slice(-2).join('.')) ? 3 : 2;
  return shared.length >= minLabels ? shared.join('.') : '';
}

// bffCookieDomain derives the cookie Domain from the BFF base URL and
// the current SPA host. Tolerates a missing/garbage URL (returns '' —
// a host-only cookie, the pre-existing behaviour).
export function bffCookieDomain(bffBaseUrl: string): string {
  let bffHost = '';
  try {
    bffHost = new URL(bffBaseUrl).hostname;
  } catch {
    return '';
  }
  return cookieScopeDomain(window.location.hostname, bffHost);
}

// clearAuthCookie expires the auth_token cookie for BOTH the host-only
// scope (legacy cookies written before the Domain fix) and the
// shared-domain scope, so a stale token can never linger after sign-out
// or a 401.
export function clearAuthCookie(bffBaseUrl: string): void {
  const expired = 'auth_token=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/';
  document.cookie = expired;
  const domain = bffCookieDomain(bffBaseUrl);
  if (domain) document.cookie = `${expired}; Domain=${domain}`;
}

// setAuthCookie writes the id_token to the `auth_token` cookie with
// the same name + path the BFF's cookie-to-auth CFFn reads. The
// max-age is bounded at 30 minutes; the JWT's own `exp` claim is
// the authoritative cap (Cognito client config sets it to 60min by
// default, and we expire the cookie a bit earlier so the BFF never
// sees an expired-but-still-cookied request).
function setAuthCookie(idToken: string, bffBaseUrl: string): void {
  const maxAge = 60 * 30; // 30 minutes
  const sameSite = 'Lax';
  const secure = window.location.protocol === 'https:' ? '; Secure' : '';
  const domain = bffCookieDomain(bffBaseUrl);
  const domainAttr = domain ? `; Domain=${domain}` : '';
  document.cookie = `auth_token=${idToken}; Path=/${domainAttr}; Max-Age=${maxAge}; SameSite=${sameSite}${secure}`;
}

// generateCodeVerifier produces a random 64-character base64url
// string. The spec allows 43–128 chars from the unreserved set
// [A-Za-z0-9-._~]; 64 chars of base64url gives 384 bits of entropy
// which is comfortably above the spec's 256-bit minimum.
function generateCodeVerifier(): string {
  const bytes = new Uint8Array(48);
  crypto.getRandomValues(bytes);
  return base64urlEncode(bytes);
}

// codeChallenge derives the S256 challenge from the verifier. The
// challenge is the base64url-encoded SHA-256 of the verifier bytes
// (per RFC 7636 § 4.2).
async function codeChallenge(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
  return base64urlEncode(new Uint8Array(digest));
}

function base64urlEncode(bytes: Uint8Array): string {
  let bin = '';
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
