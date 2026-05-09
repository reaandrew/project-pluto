// Login is the entry point for unauthenticated operators. Iter 0.G.2
// wires it to redirect into the Cognito Hosted UI using the URL produced
// from runtime config. For now the route exists but the redirect is a
// placeholder — clicking through any guard-protected page will land
// here, and 0.G.2 turns this into an immediate `window.location.replace`.
export default function Login() {
  return (
    <div>
      <h2>Sign in</h2>
      <p style={{ color: '#666' }}>
        The operator login flow redirects to the Cognito Hosted UI; the
        wiring lands in iter 0.G.2.
      </p>
    </div>
  );
}
