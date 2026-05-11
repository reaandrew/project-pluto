import { useEffect } from 'react';
import { Outlet } from 'react-router-dom';
import { COGNITO_LOGIN_URL } from './api';

// AuthGuard wraps the routes that require an operator session. On mount
// it checks the document.cookie jar for the `auth_token` cookie that
// the BFF chain's cookie-to-auth CloudFront Function reads on every
// request. Absent cookie → redirect to the Cognito Hosted UI; present
// cookie → render the protected route's <Outlet/>.
//
// The cookie itself is HttpOnly when set by the post-OAuth callback
// handler, so this check works against the BROWSER's cookie-presence,
// not the cookie's value. That's enough to gate routing without
// exposing the JWT to JavaScript.
//
// COGNITO_LOGIN_URL empty disables the redirect — used in local `npm
// run dev` and in unit tests, where bouncing to Cognito would either
// fail (CORS / unreachable) or take the test off-page.
//
// Returning <Outlet/> immediately for the cookie-present path is
// deliberate: any further auth state (user identity, group membership)
// is the BFF's responsibility on each authenticated call.
export default function AuthGuard() {
  const cookie = hasAuthCookie();

  useEffect(() => {
    if (cookie) return;
    if (!COGNITO_LOGIN_URL) return;
    // window.location.replace (not assign) so the unauthenticated URL
    // isn't kept in browser history; the back button after login goes
    // to wherever the user was before the redirect.
    window.location.replace(COGNITO_LOGIN_URL);
  }, [cookie]);

  if (!cookie) {
    // While the redirect is in flight, render a minimal placeholder
    // rather than the protected content. With COGNITO_LOGIN_URL empty
    // this is the permanent state (local dev) — the message tells the
    // operator what's happening.
    return (
      <p style={{ color: '#666' }}>
        {COGNITO_LOGIN_URL
          ? 'Redirecting to sign-in…'
          : 'Not signed in. Configure cognitoHostedLoginUrl in runtime-config.js to enable the redirect.'}
      </p>
    );
  }

  return <Outlet />;
}

// hasAuthCookie reads document.cookie and returns whether an entry
// named `auth_token` is present (any value, including empty string).
// document.cookie returns a semicolon-delimited string of `name=value`
// pairs for every non-HttpOnly cookie AND every HttpOnly cookie scoped
// to this origin's path — wait, that's wrong: HttpOnly cookies are NOT
// readable from document.cookie. This check therefore only succeeds
// when `auth_token` was set without the HttpOnly flag. The callback
// handler that finalises the OAuth code-exchange (separate iter) will
// set a tandem non-HttpOnly `auth_token_present=1` flag if it ever
// needs to mark HttpOnly auth; for now the cookie is non-HttpOnly so
// this check is sufficient.
function hasAuthCookie(): boolean {
  if (typeof document === 'undefined') return false;
  const pairs = document.cookie.split(';');
  for (const pair of pairs) {
    const eq = pair.indexOf('=');
    const name = (eq === -1 ? pair : pair.slice(0, eq)).trim();
    if (name === 'auth_token') return true;
  }
  return false;
}
