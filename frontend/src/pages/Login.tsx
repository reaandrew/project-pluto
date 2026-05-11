import { useEffect } from 'react';
import { COGNITO_LOGIN_URL } from '../api';

// /login is the explicit "Sign in" destination. On mount it bounces to
// the Cognito Hosted UI. The AuthGuard does the same thing implicitly
// for any protected route, so most callers never see this page —
// /login is the destination of an explicit click on the "Sign in" nav
// link, or the fallback when COGNITO_LOGIN_URL isn't configured.
export default function Login() {
  useEffect(() => {
    if (COGNITO_LOGIN_URL) {
      window.location.replace(COGNITO_LOGIN_URL);
    }
  }, []);

  return (
    <div>
      <h2>Sign in</h2>
      <p style={{ color: '#666' }}>
        {COGNITO_LOGIN_URL
          ? 'Redirecting to the operator sign-in page…'
          : 'Cognito Hosted UI URL not configured in runtime-config.js. Sign-in is disabled.'}
      </p>
    </div>
  );
}
