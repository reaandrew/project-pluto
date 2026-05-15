import { useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import {
  approveSpec,
  approveWebsite,
  getCandidate,
  getCandidateWebsite,
  patchSpec,
  rejectSpec,
  rejectWebsite,
  type CandidateResponse,
  type Spec,
  type SpecV1Content,
  type WebsiteView,
} from '../api';
import AccessStrip from '../components/AccessStrip';

// Candidate is the iter 4.3 /queue/[id] page — the operator's spec
// review surface. Loads {business, spec} from the api-specs BFF and
// drives the three capture-path actions (Approve / Save edits / Reject).
//
// V1 scope: header + structured viewer + JSON editor + action bar.
// Out-of-scope for 4.3 (lands in later iters):
//   - Original-site iframe + preview iframe (iter 5 publishes previews
//     to R2; until then there's nothing to render in the right pane).
//   - Access strip (passcode/preview URL — iter 5).
//   - Contact panel (iter 6.x contact enrichment).
//
// The editor is a JSON textarea, not a per-section structured editor.
// The BFF runs `ValidateSpecV1Structural` on the body and returns 400
// with the violation string when the JSON drifts — the page surfaces
// that inline.
export default function Candidate() {
  const { businessId } = useParams<{ businessId: string }>();
  const [data, setData] = useState<CandidateResponse | null>(null);
  const [editing, setEditing] = useState(false);
  const [editorContent, setEditorContent] = useState<string>('');
  const [editorParseError, setEditorParseError] = useState<string | null>(null);
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState<'idle' | 'saving' | 'approving' | 'rejecting'>('idle');
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [website, setWebsite] = useState<WebsiteView | null>(null);
  const [webBusy, setWebBusy] = useState<'idle' | 'approving' | 'rejecting'>('idle');
  const [webError, setWebError] = useState<string | null>(null);

  useEffect(() => {
    if (!businessId) {
      setError('Missing businessId in route');
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    getCandidate(businessId)
      .then((d) => {
        if (cancelled) return;
        setData(d);
        if (d.spec) {
          setEditorContent(JSON.stringify(d.spec.content, null, 2));
        }
      })
      .catch((e: Error) => {
        if (!cancelled) setError(e.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    // Website is independent of the spec view — a failure here must not
    // block spec review (a business may have a spec but no published
    // preview yet).
    getCandidateWebsite(businessId)
      .then((d) => {
        if (!cancelled) setWebsite(d.website ?? null);
      })
      .catch((e: Error) => {
        if (!cancelled) setWebError(e.message);
      });
    return () => {
      cancelled = true;
    };
  }, [businessId]);

  async function onApproveWebsite() {
    if (!businessId || !website) return;
    setWebBusy('approving');
    setWebError(null);
    try {
      setWebsite(await approveWebsite(businessId, website.id, notes || undefined));
      setNotes('');
    } catch (e) {
      setWebError((e as Error).message);
    } finally {
      setWebBusy('idle');
    }
  }

  async function onRejectWebsite() {
    if (!businessId || !website) return;
    setWebBusy('rejecting');
    setWebError(null);
    try {
      setWebsite(await rejectWebsite(businessId, website.id, notes || undefined));
      setNotes('');
    } catch (e) {
      setWebError((e as Error).message);
    } finally {
      setWebBusy('idle');
    }
  }

  function refreshSpec(updated: Spec) {
    setData((d) => (d ? { ...d, spec: updated } : d));
    setEditorContent(JSON.stringify(updated.content, null, 2));
    setEditing(false);
    setEditorParseError(null);
    setNotes('');
  }

  function parseEditorContent(): SpecV1Content | null {
    try {
      const parsed = JSON.parse(editorContent) as SpecV1Content;
      setEditorParseError(null);
      return parsed;
    } catch (e) {
      setEditorParseError((e as Error).message);
      return null;
    }
  }

  async function onSave() {
    if (!businessId || !data?.spec) return;
    const content = parseEditorContent();
    if (!content) return;
    setBusy('saving');
    setError(null);
    try {
      const updated = await patchSpec(businessId, data.spec.id, content, notes || undefined);
      refreshSpec(updated);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy('idle');
    }
  }

  async function onApprove() {
    if (!businessId || !data?.spec) return;
    setBusy('approving');
    setError(null);
    try {
      const updated = await approveSpec(businessId, data.spec.id, notes || undefined);
      refreshSpec(updated);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy('idle');
    }
  }

  async function onReject() {
    if (!businessId || !data?.spec) return;
    setBusy('rejecting');
    setError(null);
    try {
      const updated = await rejectSpec(businessId, data.spec.id, notes || undefined);
      refreshSpec(updated);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy('idle');
    }
  }

  if (loading) return <p>Loading candidate…</p>;
  if (error && !data) return <p style={{ color: 'crimson' }}>Could not load candidate: {error}</p>;
  if (!data) return <p>No data.</p>;

  const { business, spec } = data;
  const disabled = busy !== 'idle' || !spec || spec.status !== 'draft';

  return (
    <div>
      <h2 style={{ marginBottom: '0.25rem' }}>{business.name}</h2>
      <p style={{ margin: 0, color: '#666' }}>
        {business.domain} · {business.vertical || 'no vertical'} ·{' '}
        {business.location || 'no location'}
        <span
          style={{
            marginLeft: '0.75rem',
            padding: '0.1rem 0.4rem',
            background: '#eef',
            borderRadius: 3,
          }}
        >
          status: {business.status}
        </span>
      </p>

      {!spec && (
        <p style={{ marginTop: '1.5rem', color: '#666' }}>
          No spec generated for this business yet. The spec-generator runs on{' '}
          <code>website.qualified</code>; check the queue once the audit + qualifier pipeline has
          processed this business.
        </p>
      )}

      {spec && (
        <>
          <section style={{ marginTop: '1.5rem' }}>
            <h3 style={{ marginBottom: '0.25rem' }}>
              Spec
              <span style={{ marginLeft: '0.5rem', fontSize: '0.85rem', color: '#666' }}>
                v{spec.version} · {spec.status} · {spec.modelId} · {spec.promptId}
              </span>
            </h3>
            <SpecSummary spec={spec} />
          </section>

          <section style={{ marginTop: '1.5rem' }}>
            <h3 style={{ marginBottom: '0.5rem' }}>Editor (JSON)</h3>
            <p style={{ color: '#666', margin: '0 0 0.5rem' }}>
              Edit the JSON below and Save to capture an `edit` feedback event. Approve / Reject
              capture the original payload as-is.
            </p>
            <textarea
              data-testid="spec-editor"
              value={editorContent}
              readOnly={!editing || disabled}
              onChange={(e) => setEditorContent(e.target.value)}
              onFocus={() => setEditing(true)}
              spellCheck={false}
              style={{
                width: '100%',
                minHeight: '320px',
                fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                fontSize: '0.85rem',
                padding: '0.5rem',
                border: '1px solid #ddd',
                borderRadius: 4,
                background: editing ? '#fff' : '#fafafa',
              }}
            />
            {editorParseError && (
              <p style={{ color: 'crimson', marginTop: '0.25rem' }}>
                JSON parse error: {editorParseError}
              </p>
            )}
          </section>

          <section style={{ marginTop: '1.5rem' }}>
            <h3 style={{ marginBottom: '0.5rem' }}>Notes (captured with the feedback event)</h3>
            <textarea
              data-testid="spec-notes"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="Optional — why are you approving / editing / rejecting?"
              style={{
                width: '100%',
                minHeight: '80px',
                padding: '0.5rem',
                border: '1px solid #ddd',
                borderRadius: 4,
              }}
            />
          </section>

          <section
            style={{
              marginTop: '1.5rem',
              padding: '1rem',
              background: '#f9f9f9',
              borderRadius: 4,
              display: 'flex',
              gap: '0.5rem',
              alignItems: 'center',
            }}
          >
            <button onClick={onApprove} disabled={disabled}>
              {busy === 'approving' ? 'Approving…' : 'Approve'}
            </button>
            <button onClick={onSave} disabled={disabled || !editing}>
              {busy === 'saving' ? 'Saving…' : 'Save edits'}
            </button>
            <button onClick={onReject} disabled={disabled}>
              {busy === 'rejecting' ? 'Rejecting…' : 'Reject'}
            </button>
            {spec.status !== 'draft' && (
              <span style={{ marginLeft: 'auto', color: '#666', fontSize: '0.85rem' }}>
                Spec is {spec.status}. Actions disabled.
              </span>
            )}
          </section>

          {error && (
            <p data-testid="action-error" style={{ color: 'crimson', marginTop: '0.5rem' }}>
              {error}
            </p>
          )}
        </>
      )}

      <section style={{ marginTop: '2rem' }} aria-label="site-preview">
        <h3 style={{ marginBottom: '0.5rem' }}>
          Site preview
          {website && (
            <span style={{ marginLeft: '0.5rem', fontSize: '0.85rem', color: '#666' }}>
              {website.status}
            </span>
          )}
        </h3>
        {!website && !webError && (
          <p style={{ color: '#666' }}>
            No preview published yet. The generator + publisher run after the spec is approved; the
            screenshotter then captures desktop + mobile shots.
          </p>
        )}
        {webError && <p style={{ color: 'crimson' }}>Could not load preview: {webError}</p>}
        {website && (
          <>
            <AccessStrip
              previewUrl={website.previewUrl}
              cleartextRevealableUntil={
                website.passcodeRevealableUntil
                  ? new Date(website.passcodeRevealableUntil * 1000)
                  : null
              }
              onCopyUrl={(u) => void navigator.clipboard?.writeText(u)}
            />
            {website.screenshots && (
              <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', marginBottom: '1rem' }}>
                {Object.entries(website.screenshots).map(([size, url]) => (
                  <figure key={size} style={{ margin: 0 }}>
                    <img
                      src={url}
                      alt={`${size} screenshot`}
                      style={{ maxWidth: 280, border: '1px solid #ddd', borderRadius: 4 }}
                    />
                    <figcaption style={{ fontSize: '0.8rem', color: '#666' }}>{size}</figcaption>
                  </figure>
                ))}
              </div>
            )}
            <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
              <button
                onClick={onApproveWebsite}
                disabled={webBusy !== 'idle' || website.status !== 'published'}
              >
                {webBusy === 'approving' ? 'Approving…' : 'Approve preview'}
              </button>
              <button
                onClick={onRejectWebsite}
                disabled={webBusy !== 'idle' || website.status !== 'published'}
              >
                {webBusy === 'rejecting' ? 'Rejecting…' : 'Reject preview'}
              </button>
              {website.status !== 'published' && (
                <span style={{ marginLeft: 'auto', color: '#666', fontSize: '0.85rem' }}>
                  Preview is {website.status}. Actions disabled.
                </span>
              )}
            </div>
            {webError && website && (
              <p style={{ color: 'crimson', marginTop: '0.5rem' }}>{webError}</p>
            )}
          </>
        )}
      </section>
    </div>
  );
}

// SpecSummary renders a high-level read-only view of the spec — the
// operator scans it before deciding whether to edit, approve, or
// reject. The JSON editor below covers structural drill-down.
function SpecSummary({ spec }: { spec: Spec }) {
  return (
    <div
      style={{
        padding: '0.75rem 1rem',
        background: '#fafafa',
        border: '1px solid #eee',
        borderRadius: 4,
      }}
    >
      <p style={{ margin: '0 0 0.25rem' }}>
        <strong>Tone:</strong> {spec.content.brand.tone}
      </p>
      <p style={{ margin: '0 0 0.25rem' }}>
        <strong>Positioning:</strong> {spec.content.brand.positioning}
      </p>
      <p style={{ margin: '0 0 0.5rem' }}>
        <strong>Palette:</strong>{' '}
        <SwatchChip color={spec.content.brand.palette.primary} label="primary" />{' '}
        <SwatchChip color={spec.content.brand.palette.neutralDark} label="dark" />{' '}
        <SwatchChip color={spec.content.brand.palette.neutralLight} label="light" />
      </p>
      <p style={{ margin: '0 0 0.25rem' }}>
        <strong>SEO title:</strong> {spec.content.seo.title}
      </p>
      <p style={{ margin: '0 0 0.5rem' }}>
        <strong>Sections ({spec.content.page.sections.length}):</strong>{' '}
        {spec.content.page.sections.map((s) => s.type).join(' → ')}
      </p>
    </div>
  );
}

function SwatchChip({ color, label }: { color: string; label: string }) {
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 4,
        marginRight: 6,
        fontSize: '0.8rem',
      }}
    >
      <span
        aria-hidden
        style={{
          display: 'inline-block',
          width: 12,
          height: 12,
          borderRadius: 2,
          border: '1px solid #ccc',
          background: color || 'transparent',
        }}
      />
      {label}: <code>{color || '—'}</code>
    </span>
  );
}
