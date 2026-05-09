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
};

export const ENV = cfg.environment;
export const GIT_SHA = cfg.gitSha;
// Default to '/' when runtime config doesn't carry a basename — that's
// either a fresh local dev session or a production deploy from before
// this field was introduced.
export const BASENAME = cfg.basename ?? '/';

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
// Demo change for the skeleton-test PR
