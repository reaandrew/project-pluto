import { useEffect, useState } from 'react';
import { getHealth, type HealthResponse } from '../api';

// Dashboard is the root route ('/') of the admin shell. For now it surfaces
// the BFF /health response — same content the iter-0.A skeleton showed via
// pages/Hello.tsx. Iter 0.G.3+ will replace this with real pipeline KPIs
// (today's discoveries / audits / previews / queue depth, etc.).
export default function Dashboard() {
  const [data, setData] = useState<HealthResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    getHealth()
      .then((res) => {
        setData(res);
        setLoading(false);
      })
      .catch((err: Error) => {
        setError(err.message);
        setLoading(false);
      });
  }, []);

  if (loading) return <p>Loading…</p>;
  if (error) {
    return (
      <div>
        <h2>Error</h2>
        <p style={{ color: '#b00' }}>Could not reach BFF /health: {error}</p>
        <p style={{ color: '#666' }}>
          Check <code>window.__FINANCE_CONFIG__.bffBaseUrl</code> and that the API is deployed.
        </p>
      </div>
    );
  }
  return (
    <div>
      <h2>Dashboard</h2>
      <p style={{ color: '#666' }}>
        Pipeline KPIs land here in a later iteration. For now this page just proves the BFF
        round-trips.
      </p>
      <h3 style={{ marginTop: '1.5rem' }}>BFF /health</h3>
      <pre
        style={{
          background: '#0b0b0b',
          color: '#0fa',
          padding: '1rem',
          borderRadius: 6,
          overflowX: 'auto',
        }}
      >
        {JSON.stringify(data, null, 2)}
      </pre>
    </div>
  );
}
