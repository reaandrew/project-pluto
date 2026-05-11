import { Navigate, Outlet } from 'react-router-dom';
import { COGNITO_LOGIN_URL } from './api';

// AuthGuard wraps the routes that require an operator session. On
// render it checks the document.cookie jar for the `auth_token`
// cookie that the BFF chain's cookie-to-auth CloudFront Function
// reads on every request. Absent cookie → render <Navigate to=
// "/login" replace />, which lets Login.tsx do the PKCE prep before
// bouncing to Cognito. Present cookie → render the protected
// route's <Outlet/>.
//
// The cookie is set non-HttpOnly by Callback.tsx (post-OAuth code
// exchange) so document.cookie can see it. JWT validity is the
// BFF's job on each authenticated call; AuthGuard's check is
// presence-only.
//
// COGNITO_LOGIN_URL empty disables the redirect — used in local
// `npm run dev`, where bouncing through /login → Cognito would
// fail (CORS / unreachable).
export default function AuthGuard() {
  const cookie = hasAuthCookie();

  if (!cookie) {
    if (!COGNITO_LOGIN_URL) {
      return (
        <p style={{ color: '#666' }}>
          Not signed in. Configure cognitoHostedLoginUrl in runtime-config.js to enable the
          redirect.
        </p>
      );
    }
    // Internal navigation to /login so Login.tsx's PKCE prep runs
    // before the external Cognito redirect. `replace` keeps the
    // protected URL out of history.
    return <Navigate to="/login" replace />;
  }
  return <Outlet />;
}

// hasAuthCookie reads document.cookie and returns whether an entry
// named `auth_token` is present. document.cookie skips HttpOnly
// cookies — fine here because Callback.tsx sets the cookie from
// JS, so it's non-HttpOnly by construction.
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
