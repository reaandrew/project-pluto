import { useCallback, useEffect, useState } from 'react';
import { getReplies, reclassifyReply, type ReplyItem } from '../api';

// Replies is the reply-triage operator inbox (iter 8.5.3). The
// api-replies BFF lists ReplyTriage items (default the operator_inbox
// — what Bedrock wasn't confident enough to auto-action) newest-first
// via gsi1; the operator can filter by category and manually
// reclassify, which records the override and re-applies the
// Business.status side-effect for attributed replies.

const CATEGORIES = ['unsubscribe', 'positive_interest', 'unknown'] as const;
const STATES = ['operator_inbox', 'auto_actioned', 'reviewed'] as const;

export default function Replies() {
  const [items, setItems] = useState<ReplyItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [fState, setFState] = useState<string>('operator_inbox');
  const [fCategory, setFCategory] = useState<string>('');

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getReplies({ status: fState, category: fCategory || undefined })
      .then((r) => setItems(r.items))
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  }, [fState, fCategory]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getReplies({ status: fState, category: fCategory || undefined })
      .then((r) => !cancelled && setItems(r.items))
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [fState, fCategory]);

  async function applyReclassify(item: ReplyItem, newCategory: string, notes: string) {
    setBusy(item.id);
    setError(null);
    try {
      await reclassifyReply(item.id, item.ref, newCategory, notes);
      // It leaves whatever list it was in (becomes "reviewed").
      setItems((prev) => prev.filter((i) => i.id !== item.id));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  const header = <h1>Replies</h1>;

  return (
    <div style={{ padding: 16 }}>
      {header}
      <div style={{ display: 'flex', gap: 12, marginBottom: 16 }}>
        <label>
          Inbox{' '}
          <select value={fState} onChange={(e) => setFState(e.target.value)}>
            {STATES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <label>
          Category{' '}
          <select value={fCategory} onChange={(e) => setFCategory(e.target.value)}>
            <option value="">all</option>
            {CATEGORIES.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        </label>
        <button onClick={load} disabled={loading}>
          Refresh
        </button>
      </div>

      {error && <p style={{ color: 'crimson' }}>Error: {error}</p>}
      {loading && <p>Loading replies…</p>}
      {!loading && items.length === 0 && <p>No replies to review.</p>}

      <ul style={{ listStyle: 'none', padding: 0 }}>
        {items.map((item) => (
          <ReplyCard
            key={item.id}
            item={item}
            busy={busy === item.id}
            onReclassify={applyReclassify}
          />
        ))}
      </ul>
    </div>
  );
}

function ReplyCard({
  item,
  busy,
  onReclassify,
}: {
  item: ReplyItem;
  busy: boolean;
  onReclassify: (item: ReplyItem, newCategory: string, notes: string) => void;
}) {
  const [newCat, setNewCat] = useState<string>(item.category || 'unknown');
  const [notes, setNotes] = useState('');

  return (
    <li
      style={{
        border: '1px solid #ddd',
        borderRadius: 6,
        padding: 12,
        marginBottom: 12,
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 13 }}>
        <span>{new Date(item.createdAt).toLocaleString()}</span>
        <span>
          <strong>{item.category}</strong> ({Math.round(item.confidence * 100)}%)
          {item.businessId ? ` · business ${item.businessId}` : ' · unattributed'}
        </span>
      </div>
      {item.rationale && (
        <p style={{ fontStyle: 'italic', margin: '6px 0', color: '#555' }}>{item.rationale}</p>
      )}
      <blockquote
        style={{
          margin: '6px 0',
          padding: 8,
          background: '#f7f7f7',
          whiteSpace: 'pre-wrap',
          fontFamily: 'monospace',
          fontSize: 13,
        }}
      >
        {item.excerpt}
      </blockquote>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 8 }}>
        <label>
          Reclassify as{' '}
          <select
            value={newCat}
            onChange={(e) => setNewCat(e.target.value)}
            aria-label={`reclassify ${item.id}`}
          >
            {CATEGORIES.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        </label>
        <input
          placeholder="notes (optional)"
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          style={{ flex: 1 }}
        />
        <button disabled={busy} onClick={() => onReclassify(item, newCat, notes)}>
          {busy ? 'Saving…' : 'Apply'}
        </button>
      </div>
    </li>
  );
}
