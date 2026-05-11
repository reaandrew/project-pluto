import { useState } from 'react';

// AccessStrip is the operator-facing surface above the preview iframe on
// /queue/[id] (iter 5+). It surfaces the preview URL and the 8-char
// Crockford-Base32 passcode the recipient needs to type into the
// Cloudflare Worker form, plus the four actions documented in
// .ralph/specs/08-admin-ui.md § "Single candidate":
//
//   Access:  previews.<base>/sites/abcd-1234   [Copy URL]
//   Code:    H7Q3-2KX9                          [Copy code]   [Show/Hide]   [Regenerate code]
//   Cleartext available for: 4d 12h
//
// Iter 0.G.4 lands the skeleton: the layout, the props, the empty-state
// rendering, and inert action buttons that fire their callbacks but
// don't yet hook into anything real. The actual wiring (issuing a
// passcode, KMS-encrypted cleartext storage, the regenerate flow) is
// iter 5.3 (publisher) + 6.3 (the queue-detail page that mounts this).

export interface AccessStripProps {
  previewUrl?: string;
  passcode?: string;
  // Cleartext is wiped 7d after publish (passcodeRevealableUntil on
  // the Website item — see .ralph/specs/02-data-model.md). null/undefined
  // here means "no countdown yet" (no preview generated); a Date in the
  // past renders the "wiped — regenerate to view" message.
  cleartextRevealableUntil?: Date | null;
  onCopyUrl?: (url: string) => void;
  onCopyCode?: (code: string) => void;
  onRegenerateCode?: () => void;
}

export default function AccessStrip(props: AccessStripProps) {
  const [showCode, setShowCode] = useState(false);

  if (!props.previewUrl) {
    return (
      <div style={styles.empty} role="status" aria-label="access-strip-empty">
        No preview generated yet. Generate a preview to see the access URL and passcode here.
      </div>
    );
  }

  const cleartextWiped =
    !!props.cleartextRevealableUntil && props.cleartextRevealableUntil.getTime() <= Date.now();
  const codeAvailable = !!props.passcode && !cleartextWiped;

  return (
    <div style={styles.strip} aria-label="access-strip">
      <div style={styles.row}>
        <span style={styles.label}>Access:</span>
        <code style={styles.url}>{props.previewUrl}</code>
        <button
          type="button"
          style={styles.btn}
          onClick={() => props.onCopyUrl?.(props.previewUrl ?? '')}
        >
          Copy URL
        </button>
      </div>
      <div style={styles.row}>
        <span style={styles.label}>Code:</span>
        <code style={styles.code} aria-label="passcode">
          {codeAvailable
            ? showCode
              ? props.passcode
              : '••••-••••'
            : 'Code wiped — regenerate to view'}
        </code>
        {codeAvailable && (
          <>
            <button
              type="button"
              style={styles.btn}
              onClick={() => props.onCopyCode?.(props.passcode ?? '')}
            >
              Copy code
            </button>
            <button type="button" style={styles.btn} onClick={() => setShowCode((v) => !v)}>
              {showCode ? 'Hide' : 'Show'}
            </button>
          </>
        )}
        <button type="button" style={styles.btn} onClick={() => props.onRegenerateCode?.()}>
          Regenerate code
        </button>
      </div>
      <div style={styles.countdown}>
        {props.cleartextRevealableUntil ? (
          cleartextWiped ? (
            <>Cleartext wiped.</>
          ) : (
            <>Cleartext available for: {formatRemaining(props.cleartextRevealableUntil)}</>
          )
        ) : (
          <>No cleartext window set.</>
        )}
      </div>
    </div>
  );
}

// formatRemaining renders a Date as "Nd Mh" relative to now. Used for
// the cleartext-window countdown. Negative durations are coerced to 0.
// Exposed for testing.
export function formatRemaining(target: Date): string {
  const ms = Math.max(0, target.getTime() - Date.now());
  const totalHours = Math.floor(ms / (60 * 60 * 1000));
  const days = Math.floor(totalHours / 24);
  const hours = totalHours % 24;
  return `${days}d ${hours}h`;
}

const styles = {
  empty: {
    color: '#666',
    fontStyle: 'italic',
    padding: '0.75rem',
    border: '1px dashed #ccc',
    borderRadius: 4,
    margin: '1rem 0',
  } as const,
  strip: {
    padding: '0.75rem',
    border: '1px solid #ddd',
    borderRadius: 4,
    margin: '1rem 0',
    fontFamily: 'system-ui, sans-serif',
  } as const,
  row: {
    display: 'flex',
    alignItems: 'center',
    gap: '0.5rem',
    marginBottom: '0.25rem',
  } as const,
  label: { color: '#666', minWidth: 60 } as const,
  url: { background: '#f3f3f3', padding: '0.1rem 0.4rem', borderRadius: 3 } as const,
  code: {
    background: '#f3f3f3',
    padding: '0.1rem 0.4rem',
    borderRadius: 3,
    minWidth: 110,
    display: 'inline-block',
  } as const,
  btn: {
    padding: '0.2rem 0.6rem',
    fontSize: '0.85rem',
    background: '#fff',
    border: '1px solid #ccc',
    borderRadius: 3,
    cursor: 'pointer',
  } as const,
  countdown: { color: '#666', fontSize: '0.9rem', marginTop: '0.25rem' } as const,
};
