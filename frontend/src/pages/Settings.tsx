// Settings is the operator-facing edit surface for the singleton
// PipelineSettings row (master kill switch, per-stage flags, cap sliders,
// budget caps). Reads/writes go via GET/PATCH /settings on the BFF —
// shipped in iter 0.F.2. The actual form lands in iter 0.G.3; for now
// this page just confirms the route exists.
export default function Settings() {
  return (
    <div>
      <h2>Pipeline settings</h2>
      <p style={{ color: '#666' }}>
        Master kill switch, per-stage flags, cap sliders, and budget caps will live here. Backed by{' '}
        <code>GET /settings</code> /<code> PATCH /settings</code> (operator-only via Cognito group).
      </p>
    </div>
  );
}
