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

export interface HealthResponse {
  message: string;
  env: string;
  ts: number;
  items_table: string;
  git_sha?: string;
}

export async function getHealth(): Promise<HealthResponse> {
  const res = await fetch(`${cfg.bffBaseUrl}/health`, {
    credentials: 'include',
    headers: { Accept: 'application/json' },
  });
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
  const res = await fetch(`${cfg.bffBaseUrl}/settings`, {
    credentials: 'include',
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) {
    throw new Error(`HTTP ${res.status} from ${cfg.bffBaseUrl}/settings`);
  }
  return (await res.json()) as PipelineSettings;
}

export async function patchSettings(patch: Partial<PipelineSettings>): Promise<PipelineSettings> {
  const res = await fetch(`${cfg.bffBaseUrl}/settings`, {
    method: 'PATCH',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(patch),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`HTTP ${res.status} from PATCH ${cfg.bffBaseUrl}/settings: ${text}`);
  }
  return (await res.json()) as PipelineSettings;
}
