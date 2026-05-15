import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  approveWebsite,
  getCandidateWebsite,
  getQueue,
  getSettings,
  regenerateSite,
  rejectWebsite,
  type QueueItem,
  type WebsiteView,
} from '../api';

// Queue is the operator's primary review surface (iter 6.2). It lists
// Businesses awaiting review, ordered by priorityScore desc (the
// api-queue BFF does the ordering via gsi1), with per-card screenshot
// thumbnails, client-side filters, an inline action bar, daily-cap
// awareness, and cursor pagination.
//
// Scope notes:
//  - Decision actions reuse the api-website BFF (iter 5.6) keyed on the
//    item's lastWebsiteId. The richer spec editor stays on the
//    /queue/:businessId detail page (Candidate.tsx).
//  - "Snooze" (iter-6 acceptance) is deferred: no awaitingReviewUntil
//    endpoint exists yet.
//  - "X reviewed of N today": N = caps.maxReviewQueueSize; X counts
//    actions taken in this session (a persisted daily metric is iter 11
//    analytics) — labelled accordingly so it doesn't overclaim.

const REJECT_REASONS = [
  'not_my_audience',
  'too_small',
  'too_large',
  'existing_site_already_good',
  'no_reachable_contact',
  'wrong_country',
  'manually_excluded',
] as const;

export default function Queue() {
  const [items, setItems] = useState<QueueItem[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reviewQueueCap, setReviewQueueCap] = useState<number | null>(null);
  const [reviewedThisSession, setReviewedThisSession] = useState(0);

  // Filters (client-side over loaded items).
  const [fVertical, setFVertical] = useState('');
  const [fLocation, setFLocation] = useState('');
  const [fMinPriority, setFMinPriority] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getQueue({ status: 'awaiting_review' })
      .then((r) => {
        if (cancelled) return;
        setItems(r.items);
        setCursor(r.nextCursor);
      })
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setLoading(false));
    // Daily-cap awareness — best-effort; a failure here must not block
    // the queue itself.
    getSettings()
      .then((s) => !cancelled && setReviewQueueCap(s.caps.maxReviewQueueSize))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, []);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    try {
      const r = await getQueue({ status: 'awaiting_review', cursor });
      setItems((prev) => [...prev, ...r.items]);
      setCursor(r.nextCursor);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoadingMore(false);
    }
  }

  function removeItem(id: string) {
    setItems((prev) => prev.filter((i) => i.id !== id));
    setReviewedThisSession((n) => n + 1);
  }

  const verticals = useMemo(
    () => Array.from(new Set(items.map((i) => i.vertical).filter(Boolean))).sort(),
    [items]
  );

  const visible = useMemo(
    () =>
      items.filter(
        (i) =>
          (!fVertical || i.vertical === fVertical) &&
          (!fLocation || i.location.toLowerCase().includes(fLocation.toLowerCase())) &&
          i.priorityScore >= fMinPriority
      ),
    [items, fVertical, fLocation, fMinPriority]
  );

  // The page heading always renders (during load / error too) so the
  // route is identifiable and the smoke test stays stable.
  const header = (
    <>
      <h2 style={{ marginBottom: '0.25rem' }}>Review queue</h2>
      <p style={{ margin: '0 0 1rem', color: '#666' }}>
        {visible.length} shown
        {items.length !== visible.length ? ` (of ${items.length} loaded)` : ''} ·{' '}
        <strong data-testid="reviewed-count">{reviewedThisSession}</strong> reviewed this session
        {reviewQueueCap != null ? ` · queue cap ${reviewQueueCap}` : ''}
      </p>
    </>
  );

  if (loading)
    return (
      <div>
        {header}
        <p>Loading queue…</p>
      </div>
    );
  if (error && items.length === 0)
    return (
      <div>
        {header}
        <p style={{ color: 'crimson' }}>Could not load queue: {error}</p>
      </div>
    );

  return (
    <div>
      {header}

      <fieldset
        style={{
          display: 'flex',
          gap: '1rem',
          alignItems: 'flex-end',
          flexWrap: 'wrap',
          border: '1px solid #eee',
          borderRadius: 4,
          padding: '0.75rem',
          marginBottom: '1rem',
        }}
      >
        <label style={{ fontSize: '0.85rem' }}>
          Vertical
          <br />
          <select
            value={fVertical}
            onChange={(e) => setFVertical(e.target.value)}
            aria-label="filter-vertical"
          >
            <option value="">All</option>
            {verticals.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        </label>
        <label style={{ fontSize: '0.85rem' }}>
          Location contains
          <br />
          <input
            value={fLocation}
            onChange={(e) => setFLocation(e.target.value)}
            aria-label="filter-location"
            placeholder="e.g. Manchester"
          />
        </label>
        <label style={{ fontSize: '0.85rem' }}>
          Min priority: {fMinPriority.toFixed(2)}
          <br />
          <input
            type="range"
            min={0}
            max={1}
            step={0.01}
            value={fMinPriority}
            onChange={(e) => setFMinPriority(Number(e.target.value))}
            aria-label="filter-min-priority"
          />
        </label>
      </fieldset>

      {visible.length === 0 ? (
        <p style={{ color: '#666' }}>No candidates match the current filters.</p>
      ) : (
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fill, minmax(360px, 1fr))',
            gap: '1rem',
          }}
        >
          {visible.map((item) => (
            <QueueCard
              key={item.id}
              item={item}
              onResolved={() => removeItem(item.id)}
              onError={(m) => setError(m)}
            />
          ))}
        </div>
      )}

      {cursor && (
        <div style={{ marginTop: '1.5rem', textAlign: 'center' }}>
          <button onClick={loadMore} disabled={loadingMore}>
            {loadingMore ? 'Loading…' : 'Load more'}
          </button>
        </div>
      )}
      {error && items.length > 0 && (
        <p style={{ color: 'crimson', marginTop: '0.5rem' }}>{error}</p>
      )}
    </div>
  );
}

// QueueCard lazy-loads the website (thumbnail + previewUrl + websiteId)
// once on mount. The queue page is capped at maxReviewQueueSize so this
// stays bounded.
function QueueCard({
  item,
  onResolved,
  onError,
}: {
  item: QueueItem;
  onResolved: () => void;
  onError: (m: string) => void;
}) {
  const [website, setWebsite] = useState<WebsiteView | null>(null);
  const [busy, setBusy] = useState<'idle' | 'approving' | 'rejecting' | 'regen'>('idle');
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState<string>(REJECT_REASONS[0]);

  useEffect(() => {
    let cancelled = false;
    getCandidateWebsite(item.id)
      .then((r) => !cancelled && setWebsite(r.website ?? null))
      .catch(() => undefined); // thumbnail is best-effort
    return () => {
      cancelled = true;
    };
  }, [item.id]);

  const websiteId = item.lastWebsiteId || website?.id;
  const canAct = item.status === 'awaiting_review' && !!websiteId && busy === 'idle';

  async function act(kind: 'approving' | 'rejecting' | 'regen', notes?: string) {
    if (!websiteId) return;
    setBusy(kind);
    try {
      if (kind === 'approving') await approveWebsite(item.id, websiteId, notes);
      else if (kind === 'rejecting') await rejectWebsite(item.id, websiteId, notes);
      else await regenerateSite(item.id, websiteId, notes);
      onResolved();
    } catch (e) {
      onError((e as Error).message);
      setBusy('idle');
    }
  }

  const thumb = website?.screenshots?.desktop;

  return (
    <div style={{ border: '1px solid #ddd', borderRadius: 6, padding: '0.75rem' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', gap: '0.5rem' }}>
        <strong>{item.name}</strong>
        <span style={{ fontSize: '0.8rem', color: '#666' }}>
          priority {item.priorityScore.toFixed(2)}
        </span>
      </div>
      <p style={{ margin: '0.15rem 0 0.5rem', color: '#666', fontSize: '0.85rem' }}>
        {item.domain} · {item.vertical || 'no vertical'} · {item.location || 'no location'}
      </p>

      {thumb ? (
        <img
          src={thumb}
          alt={`${item.name} preview`}
          style={{
            width: '100%',
            maxHeight: 180,
            objectFit: 'cover',
            border: '1px solid #eee',
            borderRadius: 4,
          }}
        />
      ) : (
        <div
          style={{
            height: 120,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            background: '#fafafa',
            border: '1px dashed #ddd',
            borderRadius: 4,
            color: '#999',
            fontSize: '0.8rem',
          }}
        >
          no preview thumbnail
        </div>
      )}

      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.4rem', marginTop: '0.6rem' }}>
        <Link to={`/queue/${encodeURIComponent(item.id)}`}>
          <button>Review</button>
        </Link>
        <a href={`https://${item.domain}`} target="_blank" rel="noopener noreferrer">
          <button>Open original</button>
        </a>
        {website?.previewUrl && (
          <a href={website.previewUrl} target="_blank" rel="noopener noreferrer">
            <button>Open preview</button>
          </a>
        )}
      </div>

      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.4rem', marginTop: '0.5rem' }}>
        <button onClick={() => act('approving')} disabled={!canAct}>
          {busy === 'approving' ? 'Approving…' : 'Approve'}
        </button>
        <button onClick={() => act('regen')} disabled={!canAct}>
          {busy === 'regen' ? 'Regenerating…' : 'Regenerate'}
        </button>
        {!rejecting ? (
          <button onClick={() => setRejecting(true)} disabled={!canAct}>
            Reject…
          </button>
        ) : (
          <span style={{ display: 'inline-flex', gap: '0.3rem', alignItems: 'center' }}>
            <select
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              aria-label="reject-reason"
            >
              {REJECT_REASONS.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </select>
            <button onClick={() => act('rejecting', reason)} disabled={busy !== 'idle'}>
              {busy === 'rejecting' ? 'Rejecting…' : 'Confirm reject'}
            </button>
            <button onClick={() => setRejecting(false)} disabled={busy !== 'idle'}>
              Cancel
            </button>
          </span>
        )}
        {!canAct && item.status !== 'awaiting_review' && (
          <span style={{ fontSize: '0.8rem', color: '#999' }}>status: {item.status}</span>
        )}
      </div>
    </div>
  );
}
