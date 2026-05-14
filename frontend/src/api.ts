// Runtime configuration injected by /runtime-config.js (loaded before the bundle).
// CI writes the per-env values at deploy time — see scripts/deploy-frontend.sh.

export interface RuntimeConfig {
  bffBaseUrl: string;
  apiBaseUrl: string;
  environment: string;
  gitSha: string;
  // Path prefix the SPA is served from. Production = '/'; per-PR preview =
  // '/<env>'. BrowserRouter consumes this so the router's path matching
  // lines up with what the browser shows.
  basename?: string;
  // Full Cognito Hosted UI login URL with client_id, redirect_uri, scopes,
  // and response_type baked in. AuthGuard sends unauthenticated callers
  // here. Empty string at boot means "auth not configured for this env"
  // and disables the guard (useful for local `npm run dev` before the
  // operator stack is up).
  cognitoHostedLoginUrl?: string;
  // Cognito Hosted UI domain (e.g. `xyz-auth.auth.eu-west-2.amazoncognito.com`)
  // without any path — used to derive the /oauth2/token endpoint for the
  // PKCE code-exchange in Callback.tsx.
  cognitoAuthOrigin?: string;
  // Cognito app-client ID — needed in the form-encoded body of the
  // token-exchange request.
  cognitoClientId?: string;
  // Redirect URI sent in the original auth request — must be echoed back
  // exactly in the token exchange or Cognito rejects with
  // `invalid_grant`.
  cognitoRedirectUri?: string;
}

declare global {
  interface Window {
    __FINANCE_CONFIG__: RuntimeConfig;
  }
}

const cfg: RuntimeConfig = window.__FINANCE_CONFIG__ ?? {
  bffBaseUrl: 'http://localhost:8080',
  apiBaseUrl: 'http://localhost:8080',
  environment: 'unknown',
  gitSha: 'unknown',
  basename: '/',
  cognitoHostedLoginUrl: '',
  cognitoAuthOrigin: '',
  cognitoClientId: '',
  cognitoRedirectUri: '',
};

export const ENV = cfg.environment;
export const GIT_SHA = cfg.gitSha;
// Default to '/' when runtime config doesn't carry a basename — that's
// either a fresh local dev session or a production deploy from before
// this field was introduced.
export const BASENAME = cfg.basename ?? '/';
// Empty string disables the AuthGuard redirect; useful for local dev
// before the Cognito stack is reachable.
export const COGNITO_LOGIN_URL = cfg.cognitoHostedLoginUrl ?? '';

// The Callback-side fields are exposed as getters (rather than
// const captures at module-load) so the unit tests can reset
// window.__FINANCE_CONFIG__ in beforeEach and have the next call
// see the fresh values. Production callers don't care — the
// runtime-config.js loads before the bundle, so by the time these
// getters fire the window value is already set.
export function getCognitoAuthOrigin(): string {
  return window.__FINANCE_CONFIG__?.cognitoAuthOrigin ?? '';
}
export function getCognitoClientId(): string {
  return window.__FINANCE_CONFIG__?.cognitoClientId ?? '';
}
export function getCognitoRedirectUri(): string {
  return window.__FINANCE_CONFIG__?.cognitoRedirectUri ?? '';
}

// signOutAndRedirect handles the "stale auth_token cookie" case: the
// cookie is present (so AuthGuard let the user in) but the BFF's JWT
// validation rejects it with 401. Clear the cookie so future calls
// don't repeat the same dance, then bounce back through Cognito.
// Exported so individual callers can invoke it on explicit sign-out
// too (future "Sign out" link in the nav).
function signOutAndRedirect(): void {
  document.cookie = 'auth_token=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/';
  if (COGNITO_LOGIN_URL) {
    window.location.replace(COGNITO_LOGIN_URL);
  }
}

// authedFetch wraps fetch with the cookie-credentials + Accept-JSON
// boilerplate every BFF call needs, AND the 401-handling behaviour:
// a 401 response means the auth_token cookie is no longer valid
// (expired token, server rotated, tampered cookie), so we clear it
// and redirect to Cognito Hosted UI. The caller still gets a
// rejected promise so the page can render a sensible interim state
// while the redirect is in flight.
async function authedFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const res = await fetch(input, {
    credentials: 'include',
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init.headers ?? {}),
    },
  });
  if (res.status === 401) {
    signOutAndRedirect();
    throw new Error('Session expired — redirecting to sign-in');
  }
  return res;
}

export interface HealthResponse {
  message: string;
  env: string;
  ts: number;
  items_table: string;
  git_sha?: string;
}

export async function getHealth(): Promise<HealthResponse> {
  const res = await authedFetch(`${cfg.bffBaseUrl}/health`);
  if (!res.ok) {
    throw new Error(`HTTP ${res.status} from ${cfg.bffBaseUrl}/health`);
  }
  return (await res.json()) as HealthResponse;
}

// ---------------------------------------------------------------------------
// /settings — PipelineSettings read/write surface
// ---------------------------------------------------------------------------
// Shapes mirror lambdas/pkg/killswitch/settings.go. Any field added on the
// Go side has to land here too or the form will silently round-trip stale
// data. The api-settings Lambda (iter 0.F.2) deep-merges PATCH bodies, so
// sending the whole object back is safe — sending a subset only updates the
// listed fields.

export interface StageFlags {
  discoveryEnabled: boolean;
  auditEnabled: boolean;
  previewEnabled: boolean;
  outreachEnabled: boolean;
}

export interface Caps {
  maxDiscoveriesPerDay: number;
  maxAuditsPerDay: number;
  maxPreviewsPerDay: number;
  maxEmailsPerDay: number;
  maxReviewQueueSize: number;
  maxBacklogSize: number;
}

export interface Thresholds {
  minTechnicalIssueScore: number;
  minQualificationScore: number;
  minContactConfidence: number;
}

export interface Budgets {
  dailyBedrockUsd: number;
  dailyPlacesUsd: number;
  dailyEmailUsd: number;
}

export interface StagePauseReasons {
  discovery?: string;
  audit?: string;
  preview?: string;
  outreach?: string;
}

export interface PipelineSettings {
  pipelineEnabled: boolean;
  stages: StageFlags;
  caps: Caps;
  thresholds: Thresholds;
  budgets: Budgets;
  stagePauseReasons?: StagePauseReasons;
}

export async function getSettings(): Promise<PipelineSettings> {
  const res = await authedFetch(`${cfg.bffBaseUrl}/settings`);
  if (!res.ok) {
    throw new Error(`HTTP ${res.status} from ${cfg.bffBaseUrl}/settings`);
  }
  return (await res.json()) as PipelineSettings;
}

export async function patchSettings(patch: Partial<PipelineSettings>): Promise<PipelineSettings> {
  const res = await authedFetch(`${cfg.bffBaseUrl}/settings`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`HTTP ${res.status} from PATCH ${cfg.bffBaseUrl}/settings: ${text}`);
  }
  return (await res.json()) as PipelineSettings;
}

// ---------------------------------------------------------------------------
// /targeting — TargetingProfile CRUD
// ---------------------------------------------------------------------------
// Shapes mirror lambdas/pkg/targeting/profile.go. Stats counters are
// server-set; the form treats them as read-only.

export interface TargetingWeights {
  websiteAge: number;
  auditScore: number;
  businessSize: number;
  contactConfidence: number;
  verticalFit: number;
}

export interface TargetingStats {
  discovered7d: number;
  qualified7d: number;
  approved7d: number;
}

export interface TargetingProfile {
  id: string;
  vertical: string;
  location: string;
  includeKeywords: string[];
  excludeKeywords: string[];
  weights: TargetingWeights;
  enabled: boolean;
  lastRunAt?: string;
  stats: TargetingStats;
  createdAt: string;
  updatedAt: string;
  etag: string;
}

export async function listTargetingProfiles(): Promise<TargetingProfile[]> {
  const res = await authedFetch(`${cfg.bffBaseUrl}/targeting`);
  if (!res.ok) {
    throw new Error(`HTTP ${res.status} from GET ${cfg.bffBaseUrl}/targeting`);
  }
  const body = (await res.json()) as { profiles: TargetingProfile[] };
  return body.profiles ?? [];
}

// createTargetingProfile sends a partial Profile (without id/etag/etc.).
// The server fills those in and returns the canonical row.
export async function createTargetingProfile(
  draft: Omit<TargetingProfile, 'id' | 'createdAt' | 'updatedAt' | 'etag' | 'stats' | 'lastRunAt'>
): Promise<TargetingProfile> {
  const res = await authedFetch(`${cfg.bffBaseUrl}/targeting`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(draft),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`HTTP ${res.status} from POST ${cfg.bffBaseUrl}/targeting: ${text}`);
  }
  return (await res.json()) as TargetingProfile;
}

// updateTargetingProfile sends the whole edited profile back. The
// `If-Match: <etag>` header is the optimistic-concurrency check — the
// caller passes the etag they saw on the last read and the server
// rejects with 412 if the row changed in between.
export async function updateTargetingProfile(
  id: string,
  profile: TargetingProfile
): Promise<TargetingProfile> {
  const res = await authedFetch(`${cfg.bffBaseUrl}/targeting/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', 'If-Match': profile.etag },
    body: JSON.stringify(profile),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`HTTP ${res.status} from PATCH ${cfg.bffBaseUrl}/targeting/${id}: ${text}`);
  }
  return (await res.json()) as TargetingProfile;
}

export async function deleteTargetingProfile(id: string): Promise<void> {
  const res = await authedFetch(`${cfg.bffBaseUrl}/targeting/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`HTTP ${res.status} from DELETE ${cfg.bffBaseUrl}/targeting/${id}: ${text}`);
  }
}

// ---------------------------------------------------------------------------
// /metrics — discoveries widget (iter 1.4)
// ---------------------------------------------------------------------------

export interface DiscoveryRow {
  id: string;
  name: string;
  domain: string;
  vertical: string;
  location: string;
  source: string;
  confidence: number;
  status: string;
  createdAt: string;
}

export interface DiscoveriesResponse {
  recent: DiscoveryRow[];
  countsByDay: Record<string, number>;
  totalLast7Day: number;
}

export async function getDiscoveries(): Promise<DiscoveriesResponse> {
  const res = await authedFetch(`${cfg.bffBaseUrl}/metrics/discoveries`);
  if (!res.ok) {
    throw new Error(`HTTP ${res.status} from GET ${cfg.bffBaseUrl}/metrics/discoveries`);
  }
  return (await res.json()) as DiscoveriesResponse;
}

export async function runDiscoveryNow(): Promise<{ status: string; startedAt: string }> {
  const res = await authedFetch(`${cfg.bffBaseUrl}/metrics/discoveries/run`, { method: 'POST' });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(
      `HTTP ${res.status} from POST ${cfg.bffBaseUrl}/metrics/discoveries/run: ${text}`
    );
  }
  return (await res.json()) as { status: string; startedAt: string };
}

// ---------------------------------------------------------------------------
// /candidates — Spec review surface (iter 4.3)
// ---------------------------------------------------------------------------
// Shapes mirror lambdas/api-specs's SpecRow + the schemas.SpecV1 Go struct.
// Anything added on the Go side has to land here too or the candidate page
// silently round-trips stale data.

export interface SpecCTA {
  label: string;
  action: 'call' | 'email' | 'form';
}

export interface SpecSubItem {
  name?: string;
  oneLine?: string;
  q?: string;
  a?: string;
}

export interface SpecBadge {
  label: string;
}

export interface SpecSection {
  type: 'hero' | 'services' | 'about' | 'trust' | 'faq' | 'cta' | 'contact';
  headline?: string;
  subheadline?: string;
  primaryCta?: SpecCTA;
  button?: SpecCTA;
  imagePrompt?: string;
  title?: string;
  items?: SpecSubItem[];
  paragraph?: string;
  badges?: SpecBadge[];
  address?: string;
  phone?: string;
  email?: string;
  hours?: string;
}

export interface SpecPalette {
  primary: string;
  neutralDark: string;
  neutralLight: string;
}

export interface SpecBrand {
  tone: string;
  palette: SpecPalette;
  positioning: string;
}

export interface SpecSEO {
  title: string;
  description: string;
  keywords?: string[];
}

export interface SpecConstraints {
  doNotInventTestimonials: boolean;
  doNotInventAwards: boolean;
  doNotInventPrices: boolean;
}

export interface SpecV1Content {
  brand: SpecBrand;
  page: { sections: SpecSection[] };
  seo: SpecSEO;
  constraints: SpecConstraints;
}

export interface Spec {
  id: string;
  version: number;
  status: 'draft' | 'approved' | 'rejected';
  content: SpecV1Content;
  modelId: string;
  promptId: string;
  approvedBy?: string;
  approvedAt?: string;
  createdAt: string;
  updatedAt: string;
  etag: string;
}

export interface CandidateBusiness {
  id: string;
  name: string;
  domain: string;
  vertical: string;
  location: string;
  status: string;
}

export interface CandidateResponse {
  business: CandidateBusiness;
  spec?: Spec;
}

export async function getCandidate(businessId: string): Promise<CandidateResponse> {
  const url = `${cfg.bffBaseUrl}/candidates/${encodeURIComponent(businessId)}`;
  const res = await authedFetch(url);
  if (!res.ok) {
    throw new Error(`HTTP ${res.status} from GET ${url}`);
  }
  return (await res.json()) as CandidateResponse;
}

export async function patchSpec(
  businessId: string,
  specId: string,
  content: SpecV1Content,
  notes?: string
): Promise<Spec> {
  const url = `${cfg.bffBaseUrl}/candidates/${encodeURIComponent(businessId)}/specs/${encodeURIComponent(specId)}`;
  const res = await authedFetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, notes }),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`HTTP ${res.status} from PATCH ${url}: ${text}`);
  }
  return (await res.json()) as Spec;
}

export async function approveSpec(
  businessId: string,
  specId: string,
  notes?: string
): Promise<Spec> {
  return decideSpec(businessId, specId, 'approve', notes);
}

export async function rejectSpec(
  businessId: string,
  specId: string,
  notes?: string
): Promise<Spec> {
  return decideSpec(businessId, specId, 'reject', notes);
}

async function decideSpec(
  businessId: string,
  specId: string,
  decision: 'approve' | 'reject',
  notes?: string
): Promise<Spec> {
  const url = `${cfg.bffBaseUrl}/candidates/${encodeURIComponent(businessId)}/specs/${encodeURIComponent(specId)}/${decision}`;
  const res = await authedFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ notes: notes ?? '' }),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`HTTP ${res.status} from POST ${url}: ${text}`);
  }
  return (await res.json()) as Spec;
}
