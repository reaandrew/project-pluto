// Placeholder — overwritten at deploy time by scripts/deploy-frontend.sh per environment.
// Local dev uses these values when `npm run dev` is run.
window.__FINANCE_CONFIG__ = {
  bffBaseUrl: 'http://localhost:8080',
  apiBaseUrl: 'http://localhost:8080',
  environment: 'local',
  gitSha: 'dev',
  basename: '/',
  // Empty disables the AuthGuard redirect so `npm run dev` doesn't bounce
  // you out to a Cognito URL that may not be reachable from localhost.
  cognitoHostedLoginUrl: '',
};
