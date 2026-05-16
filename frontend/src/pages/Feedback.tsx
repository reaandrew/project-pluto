import { useEffect, useMemo, useState } from 'react';
import { getFeedback, type FeedbackItem } from '../api';

// Feedback is the operator-override log (iter 9.2). Every
// Approve/Edit/Reject on Spec/Website/Email writes a Feedback row
// (carry-over from iters 4–7); the api-feedback BFF lists them per
// vertical newest-first. Filters: vertical (server-side partition),
// stage/subject (server-side), and a since-date (client-side over the
// loaded page). Payload bodies stay on the row — this is a who/what/
// when log, not a diff view (that's /tuners, iter 9.4).

const SUBJECTS = ['audit', 'qualification', 'spec', 'website', 'email'] as const;

export default function Feedback() {
  const [items, setItems] = useState<FeedbackItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [vertical, setVertical] = useState('default');
  const [subject, setSubject] = useState('');
  const [since, setSince] = useState('');

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    getFeedback({ vertical, subject: subject || undefined })
      .then((r) => !cancelled && setItems(r.items))
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [vertical, subject]);

  const visible = useMemo(
    () => (since ? items.filter((i) => i.createdAt >= since) : items),
    [items, since]
  );

  return (
    <div style={{ padding: 16 }}>
      <h1>Feedback</h1>
      <div style={{ display: 'flex', gap: 12, marginBottom: 16, flexWrap: 'wrap' }}>
        <label>
          Vertical{' '}
          <input
            value={vertical}
            onChange={(e) => setVertical(e.target.value || 'default')}
            placeholder="default"
            aria-label="vertical"
          />
        </label>
        <label>
          Stage{' '}
          <select value={subject} onChange={(e) => setSubject(e.target.value)} aria-label="stage">
            <option value="">all</option>
            {SUBJECTS.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <label>
          Since{' '}
          <input
            type="date"
            value={since}
            onChange={(e) => setSince(e.target.value)}
            aria-label="since"
          />
        </label>
      </div>

      {error && <p style={{ color: 'crimson' }}>Error: {error}</p>}
      {loading && <p>Loading feedback…</p>}
      {!loading && visible.length === 0 && <p>No feedback for these filters.</p>}

      {visible.length > 0 && (
        <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: 14 }}>
          <thead>
            <tr style={{ textAlign: 'left', borderBottom: '1px solid #ccc' }}>
              <th style={{ padding: 6 }}>When</th>
              <th style={{ padding: 6 }}>Stage</th>
              <th style={{ padding: 6 }}>Action</th>
              <th style={{ padding: 6 }}>Subject</th>
              <th style={{ padding: 6 }}>Actor</th>
              <th style={{ padding: 6 }}>Notes</th>
            </tr>
          </thead>
          <tbody>
            {visible.map((i) => (
              <tr key={i.id} style={{ borderBottom: '1px solid #eee' }}>
                <td style={{ padding: 6 }}>{new Date(i.createdAt).toLocaleString()}</td>
                <td style={{ padding: 6 }}>{i.subject}</td>
                <td style={{ padding: 6 }}>{i.action}</td>
                <td style={{ padding: 6, fontFamily: 'monospace' }}>{i.subjectId}</td>
                <td style={{ padding: 6 }}>{i.actor}</td>
                <td style={{ padding: 6 }}>{i.notes ?? ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
