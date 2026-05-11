import { useEffect, useState } from 'react';
import {
  getDiscoveries,
  runDiscoveryNow,
  type DiscoveriesResponse,
  type DiscoveryRow,
} from '../api';

// /metrics is the operator's at-a-glance dashboard. Iter 1.4 lands the
// discoveries widget: a 7-day count, the most recent Business rows,
// and a "Run discovery now" button that synchronously fires the
// discover Lambda (per-domain dedup makes a manual run safe even if
// the hourly schedule fires concurrently). Funnel + spend + reply-
// rate metrics land alongside their producing iters.
export default function Metrics() {
  const [data, setData] = useState<DiscoveriesResponse | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [runStatus, setRunStatus] = useState<'idle' | 'running' | 'ok' | 'error'>('idle');
  const [runError, setRunError] = useState<string | null>(null);

  function refresh() {
    getDiscoveries()
      .then((d) => {
        setData(d);
        setLoadError(null);
      })
      .catch((err: Error) => setLoadError(err.message));
  }

  useEffect(refresh, []);

  async function onRun() {
    setRunStatus('running');
    setRunError(null);
    try {
      await runDiscoveryNow();
      setRunStatus('ok');
      // Wait a moment then refresh — the discover Lambda is sync, so
      // the rows should be visible immediately, but cap the
      // perceived-latency at one extra refresh cycle.
      refresh();
    } catch (err) {
      setRunStatus('error');
      setRunError((err as Error).message);
    }
  }

  if (loadError) {
    return (
      <div>
        <h2>Metrics</h2>
        <p style={{ color: '#b00' }}>Could not load discoveries: {loadError}</p>
      </div>
    );
  }
  if (!data) {
    return (
      <div>
        <h2>Metrics</h2>
        <p>Loading…</p>
      </div>
    );
  }

  return (
    <div>
      <h2>Metrics</h2>

      <section style={section}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '1rem' }}>
          <h3 style={h3}>Discoveries — last 7 days</h3>
          <button
            type="button"
            style={primary}
            disabled={runStatus === 'running'}
            onClick={onRun}
            aria-label="run-discovery-now"
          >
            {runStatus === 'running' ? 'Running…' : 'Run discovery now'}
          </button>
          {runStatus === 'ok' && <span style={{ color: '#070' }}>Discovery run started.</span>}
          {runStatus === 'error' && runError && (
            <span style={{ color: '#b00' }}>Run failed: {runError}</span>
          )}
        </div>
        <p style={{ color: '#666', marginTop: '0.5rem' }}>
          Total new businesses: <strong>{data.totalLast7Day}</strong> in the last 7 days.
        </p>
        <ul style={{ listStyle: 'none', padding: 0, margin: '0.5rem 0' }}>
          {orderedDays(data.countsByDay).map(([day, count]) => (
            <li key={day} style={{ fontFamily: 'monospace' }}>
              {day}: <strong>{count}</strong>
              <span style={{ color: '#aaa', marginLeft: '0.5rem' }}>{bar(count)}</span>
            </li>
          ))}
        </ul>
      </section>

      <section style={section}>
        <h3 style={h3}>Recent discoveries</h3>
        {data.recent.length === 0 ? (
          <p style={{ color: '#666' }}>
            No discoveries yet. Click "Run discovery now" or wait for the hourly schedule.
          </p>
        ) : (
          <table style={{ borderCollapse: 'collapse', width: '100%' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid #ddd', textAlign: 'left' }}>
                <th style={th}>Name</th>
                <th style={th}>Domain</th>
                <th style={th}>Source</th>
                <th style={th}>Vertical</th>
                <th style={th}>Confidence</th>
                <th style={th}>Created</th>
              </tr>
            </thead>
            <tbody>
              {data.recent.map((r: DiscoveryRow) => (
                <tr key={r.id} style={{ borderBottom: '1px solid #eee' }}>
                  <td style={td}>{r.name}</td>
                  <td style={td}>
                    <code>{r.domain}</code>
                  </td>
                  <td style={td}>{r.source}</td>
                  <td style={td}>{r.vertical}</td>
                  <td style={td}>{r.confidence.toFixed(2)}</td>
                  <td style={td}>{r.createdAt}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}

// orderedDays sorts the countsByDay map by date descending so today
// appears first. Exposed for testing.
export function orderedDays(counts: Record<string, number>): [string, number][] {
  return Object.entries(counts).sort((a, b) => (a[0] < b[0] ? 1 : -1));
}

// bar renders a tiny visual bar — keeps it pure ASCII so no Tailwind
// dependency. Caps at 30 chars so a single big day doesn't blow out
// the layout.
function bar(n: number): string {
  if (n <= 0) return '';
  const len = Math.min(n, 30);
  return '█'.repeat(len);
}

const section = { marginTop: '1.5rem' } as const;
const h3 = { margin: '0 0 0.5rem', fontSize: '1rem' } as const;
const th = { padding: '0.4rem 0.5rem', color: '#666', fontWeight: 600 } as const;
const td = { padding: '0.4rem 0.5rem' } as const;
const primary = {
  padding: '0.4rem 0.8rem',
  background: '#0a3',
  color: 'white',
  border: 'none',
  borderRadius: 4,
  cursor: 'pointer',
} as const;
