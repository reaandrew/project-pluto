import { useEffect, useState } from 'react';
import { getHealth, type HealthResponse } from '../api';

export default function Hello() {
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
      <h2>API health</h2>
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
