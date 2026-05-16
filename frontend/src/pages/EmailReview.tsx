import { useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  approveEmail,
  getCandidateEmail,
  patchEmail,
  rejectEmail,
  type CandidateEmailResponse,
  type EmailDraftView,
} from '../api';

// EmailReview is the iter 7.3 /queue/:businessId/email page. The
// operator reviews the generated outreach draft, runs the static
// checks, and Approves / Edits / Rejects. Every action emits
// feedback.captured(subject="email") server-side with the passcode
// REDACTED in the feedback payload (the BFF KMS-decrypts the cipher to
// scrub it) — the cleartext never reaches the feedback log.
//
// The operator IS authorised to see the real body here (they're the
// sender). We deliberately do NOT surface the passcode value as a
// separate field — the code lives only inline in the body, which keeps
// the cleartext surface minimal.

const WORD_CAP = 200;

export default function EmailReview() {
  const { businessId } = useParams<{ businessId: string }>();
  const [data, setData] = useState<CandidateEmailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [subject, setSubject] = useState('');
  const [bodyText, setBodyText] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState<'idle' | 'saving' | 'approving' | 'rejecting'>('idle');

  useEffect(() => {
    if (!businessId) {
      setError('Missing businessId in route');
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    getCandidateEmail(businessId)
      .then((d) => {
        if (cancelled) return;
        setData(d);
        if (d.email) {
          setSubject(d.email.subject);
          setBodyText(d.email.body);
        }
      })
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [businessId]);

  const email = data?.email ?? null;

  // Static checks the page can compute client-side. (The BFF/post-
  // validator is the source of truth; this is operator guidance.)
  const checks = useMemo(() => {
    if (!email) return [] as { label: string; ok: boolean }[];
    const words = bodyText.trim().split(/\s+/).filter(Boolean).length;
    return [
      { label: `≤ ${WORD_CAP} words (${words})`, ok: words > 0 && words <= WORD_CAP },
      { label: 'opt-out line present', ok: bodyText.includes(email.optOutLine) },
      { label: 'no "password" wording', ok: !/password/i.test(bodyText) },
      { label: 'access code referenced', ok: /access code|the code is|code:/i.test(bodyText) },
    ];
  }, [email, bodyText]);

  function refresh(updated: EmailDraftView) {
    setData((d) => (d ? { ...d, email: updated } : d));
    setSubject(updated.subject);
    setBodyText(updated.body);
    setEditing(false);
    setNotes('');
  }

  async function onSave() {
    if (!businessId || !email) return;
    setBusy('saving');
    setError(null);
    try {
      refresh(await patchEmail(businessId, email.id, subject, bodyText, notes || undefined));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy('idle');
    }
  }

  async function onApprove() {
    if (!businessId || !email) return;
    setBusy('approving');
    setError(null);
    try {
      refresh(await approveEmail(businessId, email.id, notes || undefined));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy('idle');
    }
  }

  async function onReject() {
    if (!businessId || !email) return;
    setBusy('rejecting');
    setError(null);
    try {
      refresh(await rejectEmail(businessId, email.id, notes || undefined));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy('idle');
    }
  }

  if (loading) return <p>Loading email draft…</p>;
  if (error && !data) return <p style={{ color: 'crimson' }}>Could not load email: {error}</p>;
  if (!data) return <p>No data.</p>;

  const { business } = data;
  const disabled = busy !== 'idle' || !email || email.status !== 'draft';

  return (
    <div>
      <h2 style={{ marginBottom: '0.25rem' }}>Email review — {business.name}</h2>
      <p style={{ margin: '0 0 1rem' }}>
        <Link to={`/queue/${encodeURIComponent(businessId ?? '')}`}>← back to candidate</Link>
      </p>

      {!email && (
        <p style={{ color: '#666' }}>
          No email draft yet. The email-draft Lambda runs on <code>website.approved</code>; approve
          the preview first.
        </p>
      )}

      {email && (
        <>
          <p style={{ margin: 0, color: '#666' }}>
            {email.promptId} · {email.modelId} · {email.wordCount} words ·{' '}
            <span style={{ padding: '0.1rem 0.4rem', background: '#eef', borderRadius: 3 }}>
              {email.status}
            </span>
          </p>

          <section style={{ marginTop: '1rem' }} aria-label="static-checks">
            <h3 style={{ marginBottom: '0.5rem' }}>Static checks</h3>
            <ul style={{ margin: 0, paddingLeft: '1.1rem' }}>
              {checks.map((c) => (
                <li key={c.label} style={{ color: c.ok ? '#157f3b' : '#b00020' }}>
                  {c.ok ? '✓' : '✗'} {c.label}
                </li>
              ))}
            </ul>
          </section>

          <section style={{ marginTop: '1.5rem' }}>
            <h3 style={{ marginBottom: '0.5rem' }}>Subject</h3>
            <input
              data-testid="email-subject"
              value={subject}
              readOnly={!editing || disabled}
              onChange={(e) => setSubject(e.target.value)}
              onFocus={() => !disabled && setEditing(true)}
              style={{
                width: '100%',
                padding: '0.5rem',
                border: '1px solid #ddd',
                borderRadius: 4,
                background: editing ? '#fff' : '#fafafa',
              }}
            />
          </section>

          <section style={{ marginTop: '1rem' }}>
            <h3 style={{ marginBottom: '0.5rem' }}>Body</h3>
            <p style={{ color: '#666', margin: '0 0 0.5rem', fontSize: '0.85rem' }}>
              The access code is inline in the body. Edits are captured as feedback with the code
              redacted server-side — it never reaches the feedback log.
            </p>
            <textarea
              data-testid="email-body"
              value={bodyText}
              readOnly={!editing || disabled}
              onChange={(e) => setBodyText(e.target.value)}
              onFocus={() => !disabled && setEditing(true)}
              spellCheck={false}
              style={{
                width: '100%',
                minHeight: '280px',
                fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                fontSize: '0.85rem',
                padding: '0.5rem',
                border: '1px solid #ddd',
                borderRadius: 4,
                background: editing ? '#fff' : '#fafafa',
              }}
            />
          </section>

          <section style={{ marginTop: '1rem' }}>
            <h3 style={{ marginBottom: '0.5rem' }}>Notes (captured with the feedback event)</h3>
            <textarea
              data-testid="email-notes"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="Optional — why are you approving / editing / rejecting?"
              style={{
                width: '100%',
                minHeight: '70px',
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
            {email.status !== 'draft' && (
              <span style={{ marginLeft: 'auto', color: '#666', fontSize: '0.85rem' }}>
                Email is {email.status}. Actions disabled.
              </span>
            )}
          </section>

          {error && (
            <p data-testid="email-error" style={{ color: 'crimson', marginTop: '0.5rem' }}>
              {error}
            </p>
          )}
        </>
      )}
    </div>
  );
}
