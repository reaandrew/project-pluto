import { useCallback, useEffect, useState } from 'react';
import { getTuners, applyTuner, dismissTuner, type TunerItem } from '../api';

// Tuners is the weekly-tuner review surface (iter 9.4). The three
// tuners propose PENDING profile deltas; the operator reviews the
// rationale + the proposed change (rendered as pretty JSON — the
// "diff" against the live profile) and Applies (mutates the live
// VerticalStyleGuide / EmailToneProfile + bumps its version) or
// Dismisses. No auto-apply.

export default function Tuners() {
  const [items, setItems] = useState<TunerItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getTuners('pending')
      .then((r) => setItems(r.items))
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getTuners('pending')
      .then((r) => !cancelled && setItems(r.items))
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, []);

  async function decide(item: TunerItem, kind: 'apply' | 'dismiss') {
    setBusy(item.id);
    setError(null);
    try {
      if (kind === 'apply') {
        await applyTuner(item.id, item.ref);
      } else {
        await dismissTuner(item.id, item.ref);
      }
      setItems((prev) => prev.filter((i) => i.id !== item.id));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div style={{ padding: 16 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between' }}>
        <h1>Tuners</h1>
        <button onClick={load} disabled={loading}>
          Refresh
        </button>
      </div>

      {error && <p style={{ color: 'crimson' }}>Error: {error}</p>}
      {loading && <p>Loading proposed deltas…</p>}
      {!loading && items.length === 0 && <p>No pending tuner proposals.</p>}

      <ul style={{ listStyle: 'none', padding: 0 }}>
        {items.map((item) => (
          <li
            key={item.id}
            style={{ border: '1px solid #ddd', borderRadius: 6, padding: 12, marginBottom: 12 }}
          >
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 13 }}>
              <span>
                <strong>{item.kind}</strong> · {item.vertical}
              </span>
              <span>
                {item.promptId} · {new Date(item.createdAt).toLocaleString()}
              </span>
            </div>
            <p style={{ fontStyle: 'italic', margin: '6px 0', color: '#555' }}>{item.rationale}</p>
            <pre
              style={{
                margin: '6px 0',
                padding: 8,
                background: '#f7f7f7',
                overflowX: 'auto',
                fontSize: 12,
              }}
            >
              {JSON.stringify(item.proposed, null, 2)}
            </pre>
            <div style={{ display: 'flex', gap: 8 }}>
              <button disabled={busy === item.id} onClick={() => decide(item, 'apply')}>
                {busy === item.id ? 'Working…' : 'Apply'}
              </button>
              <button disabled={busy === item.id} onClick={() => decide(item, 'dismiss')}>
                Dismiss
              </button>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
