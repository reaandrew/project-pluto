import { useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  getBffBaseUrl,
  getCognitoAuthOrigin,
  getCognitoClientId,
  getCognitoRedirectUri,
} from '../api';
import { completePkceFlow } from '../auth';

// /oauth/callback is the URL Cognito redirects back to after the
// operator authenticates. It carries `?code=<auth-code>` and we
// trade that for an id_token via the Cognito /oauth2/token
// endpoint (PKCE flow — see auth.ts). On success we set the
// `auth_token` cookie and bounce to the dashboard.
//
// The route is registered as PUBLIC (outside AuthGuard) — a caller
// who isn't authenticated yet HAS to be able to land here.
export default function Callback() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [state, setState] = useState<'pending' | 'error'>('pending');
  const [errorMsg, setErrorMsg] = useState<string | null>(null);

  useEffect(() => {
    const code = params.get('code');
    const cognitoError = params.get('error');

    if (cognitoError) {
      const desc = params.get('error_description') ?? '';
      setState('error');
      setErrorMsg(`Cognito returned ${cognitoError}: ${desc}`);
      return;
    }
    if (!code) {
      setState('error');
      setErrorMsg(
        'No `code` query parameter — this URL is only meant to be reached via the Cognito redirect.'
      );
      return;
    }
    const authOrigin = getCognitoAuthOrigin();
    const clientId = getCognitoClientId();
    const redirectUri = getCognitoRedirectUri();
    if (!authOrigin || !clientId || !redirectUri) {
      setState('error');
      setErrorMsg('Cognito auth fields missing from runtime-config.js — re-deploy and try again.');
      return;
    }

    completePkceFlow(code, authOrigin, clientId, redirectUri, getBffBaseUrl())
      .then(() => {
        // navigate('/', { replace: true }) replaces the current
        // history entry so the back button doesn't return to the
        // callback URL with the (now-used) authorization code.
        navigate('/', { replace: true });
      })
      .catch((err: Error) => {
        setState('error');
        setErrorMsg(err.message);
      });
  }, [params, navigate]);

  if (state === 'error') {
    return (
      <div>
        <h2>Sign-in failed</h2>
        <p style={{ color: '#b00' }}>{errorMsg}</p>
        <p>
          <a href="/login">Start again</a>
        </p>
      </div>
    );
  }
  return (
    <div>
      <h2>Signing you in…</h2>
      <p style={{ color: '#666' }}>
        Exchanging the Cognito authorization code for a session token.
      </p>
    </div>
  );
}
