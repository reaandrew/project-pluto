import { useEffect, useState } from 'react';
import { COGNITO_LOGIN_URL } from '../api';
import { beginPkceFlow } from '../auth';

// /login is the explicit "Sign in" destination. AuthGuard navigates
// here when an unauthenticated caller hits a protected route. On
// mount we generate the PKCE verifier + challenge, stash the
// verifier in sessionStorage, and bounce to the Cognito Hosted UI
// with the challenge appended.
//
// If COGNITO_LOGIN_URL is empty (local dev before the substrate is
// reachable) the page shows a static message instead of looping.
export default function Login() {
  const [errorMsg, setErrorMsg] = useState<string | null>(null);

  useEffect(() => {
    if (!COGNITO_LOGIN_URL) return;
    beginPkceFlow(COGNITO_LOGIN_URL)
      .then((url) => {
        window.location.replace(url);
      })
      .catch((err: Error) => {
        setErrorMsg(err.message);
      });
  }, []);

  if (errorMsg) {
    return (
      <div>
        <h2>Sign-in failed</h2>
        <p style={{ color: '#b00' }}>{errorMsg}</p>
      </div>
    );
  }

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
