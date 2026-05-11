import { useEffect, useState } from 'react';
import { getSettings, patchSettings, type PipelineSettings } from '../api';

// Settings is the operator's edit surface for the singleton
// PipelineSettings row. GET on mount; the form binds to local state;
// Save sends the whole object as a PATCH so the api-settings Lambda
// deep-merges it. The cost preview is a static computation from the
// caps using the unit costs documented in
// .ralph/specs/05-capacity-and-cost.md § Cost model — pure UI, no
// round trip — so the operator can see the impact of cap changes
// before saving.
export default function Settings() {
  const [settings, setSettings] = useState<PipelineSettings | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saveStatus, setSaveStatus] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    getSettings()
      .then((s) => setSettings(s))
      .catch((err: Error) => setLoadError(err.message));
  }, []);

  if (loadError) {
    return (
      <div>
        <h2>Pipeline settings</h2>
        <p style={{ color: '#b00' }}>Could not load settings: {loadError}</p>
      </div>
    );
  }
  if (!settings) {
    return (
      <div>
        <h2>Pipeline settings</h2>
        <p>Loading…</p>
      </div>
    );
  }

  function updateMaster(v: boolean) {
    setSettings((s) => (s ? { ...s, pipelineEnabled: v } : s));
  }
  function updateStage(key: keyof PipelineSettings['stages'], v: boolean) {
    setSettings((s) => (s ? { ...s, stages: { ...s.stages, [key]: v } } : s));
  }
  function updateCap(key: keyof PipelineSettings['caps'], v: number) {
    setSettings((s) => (s ? { ...s, caps: { ...s.caps, [key]: v } } : s));
  }
  function updateBudget(key: keyof PipelineSettings['budgets'], v: number) {
    setSettings((s) => (s ? { ...s, budgets: { ...s.budgets, [key]: v } } : s));
  }
  function updateThreshold(key: keyof PipelineSettings['thresholds'], v: number) {
    setSettings((s) => (s ? { ...s, thresholds: { ...s.thresholds, [key]: v } } : s));
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!settings) return;
    setSaveStatus('saving');
    setSaveError(null);
    try {
      const updated = await patchSettings(settings);
      setSettings(updated);
      setSaveStatus('saved');
    } catch (err) {
      setSaveStatus('error');
      setSaveError((err as Error).message);
    }
  }

  const preview = computeDailyCostUsd(settings);

  return (
    <div>
      <h2>Pipeline settings</h2>

      <form onSubmit={onSubmit}>
        <section style={section}>
          <h3 style={h3}>Master kill switch</h3>
          <label style={inlineLabel}>
            <input
              type="checkbox"
              checked={settings.pipelineEnabled}
              onChange={(e) => updateMaster(e.target.checked)}
            />
            <span>pipelineEnabled</span>
          </label>
          <p style={hint}>
            When off, every consumer Lambda short-circuits at entry and emits a{' '}
            <code>pipeline.&lt;stage&gt;.skipped_killed</code> metric.
          </p>
        </section>

        <section style={section}>
          <h3 style={h3}>Per-stage flags</h3>
          {stageRows.map(([label, key]) => (
            <label key={key} style={inlineLabel}>
              <input
                type="checkbox"
                checked={settings.stages[key]}
                onChange={(e) => updateStage(key, e.target.checked)}
              />
              <span>{label}</span>
              {settings.stagePauseReasons?.[reasonKey(key)] === 'budget' && (
                <em style={{ color: '#b46', marginLeft: '0.5rem' }}>(auto-paused: budget)</em>
              )}
            </label>
          ))}
        </section>

        <section style={section}>
          <h3 style={h3}>Daily caps</h3>
          {capRows.map(([label, key]) => (
            <label key={key} style={blockLabel}>
              <span style={{ display: 'inline-block', width: 220 }}>{label}</span>
              <input
                type="number"
                min={0}
                value={settings.caps[key]}
                onChange={(e) => updateCap(key, Number(e.target.value))}
                style={numInput}
              />
            </label>
          ))}
        </section>

        <section style={section}>
          <h3 style={h3}>Daily budget caps (USD)</h3>
          {budgetRows.map(([label, key]) => (
            <label key={key} style={blockLabel}>
              <span style={{ display: 'inline-block', width: 220 }}>{label}</span>
              <input
                type="number"
                min={0}
                step={0.01}
                value={settings.budgets[key]}
                onChange={(e) => updateBudget(key, Number(e.target.value))}
                style={numInput}
              />
            </label>
          ))}
        </section>

        <section style={section}>
          <h3 style={h3}>Thresholds</h3>
          {thresholdRows.map(([label, key, max, step]) => (
            <label key={key} style={blockLabel}>
              <span style={{ display: 'inline-block', width: 220 }}>{label}</span>
              <input
                type="number"
                min={0}
                max={max}
                step={step}
                value={settings.thresholds[key]}
                onChange={(e) => updateThreshold(key, Number(e.target.value))}
                style={numInput}
              />
            </label>
          ))}
        </section>

        <section style={section} aria-label="cost-preview">
          <h3 style={h3}>Cost preview</h3>
          <p style={hint}>
            Estimated daily AI spend from the current caps, computed against the unit costs from{' '}
            <code>.ralph/specs/05-capacity-and-cost.md</code>. Excludes the fixed Lambda / DynamoDB
            / R2 / KMS floor (~$5/month).
          </p>
          <ul style={{ listStyle: 'none', padding: 0, margin: 0 }}>
            <li>Discovery (Places API on ~30% of calls): ${preview.discoveryUsd.toFixed(2)}</li>
            <li>Audit (Bedrock Haiku): ${preview.auditUsd.toFixed(2)}</li>
            <li>Preview (Bedrock Sonnet + Cloudflare render): ${preview.previewUsd.toFixed(2)}</li>
            <li>Outreach (Bedrock Haiku + SES): ${preview.outreachUsd.toFixed(2)}</li>
            <li>
              <strong>Total daily: ${preview.totalUsd.toFixed(2)}</strong> (≈ $
              {(preview.totalUsd * 30).toFixed(0)} / month)
            </li>
          </ul>
        </section>

        <div style={{ marginTop: '1.5rem' }}>
          <button type="submit" disabled={saveStatus === 'saving'} style={saveButton}>
            {saveStatus === 'saving' ? 'Saving…' : 'Save changes'}
          </button>
          {saveStatus === 'saved' && (
            <span style={{ marginLeft: '1rem', color: '#070' }}>Saved.</span>
          )}
          {saveStatus === 'error' && saveError && (
            <span style={{ marginLeft: '1rem', color: '#b00' }}>Save failed: {saveError}</span>
          )}
        </div>
      </form>
    </div>
  );
}

// Cost-per-call table from .ralph/specs/05-capacity-and-cost.md § Cost model.
// When the spec changes, change here and update the Settings tests.
const COST_PER_CALL = {
  googlePlaces: 0.017,
  bedrockHaikuAudit: 0.012,
  bedrockSonnetSpec: 0.075,
  cloudflareRender: 0.0005,
  bedrockHaikuEmail: 0.005,
  ses: 0.0001,
};

// computeDailyCostUsd derives per-stage and total daily spend that the
// configured caps would allow, using the unit costs above. Assumes
// Discovery hits Places on 30% of calls (matches the spec's example).
// Exposed for testing.
export function computeDailyCostUsd(s: PipelineSettings): {
  discoveryUsd: number;
  auditUsd: number;
  previewUsd: number;
  outreachUsd: number;
  totalUsd: number;
} {
  const c = s.caps;
  const discoveryUsd = c.maxDiscoveriesPerDay * 0.3 * COST_PER_CALL.googlePlaces;
  const auditUsd = c.maxAuditsPerDay * COST_PER_CALL.bedrockHaikuAudit;
  const previewUsd =
    c.maxPreviewsPerDay * (COST_PER_CALL.bedrockSonnetSpec + COST_PER_CALL.cloudflareRender);
  const outreachUsd = c.maxEmailsPerDay * (COST_PER_CALL.bedrockHaikuEmail + COST_PER_CALL.ses);
  return {
    discoveryUsd,
    auditUsd,
    previewUsd,
    outreachUsd,
    totalUsd: discoveryUsd + auditUsd + previewUsd + outreachUsd,
  };
}

const stageRows: ReadonlyArray<readonly [string, keyof PipelineSettings['stages']]> = [
  ['Discovery', 'discoveryEnabled'],
  ['Audit', 'auditEnabled'],
  ['Preview', 'previewEnabled'],
  ['Outreach', 'outreachEnabled'],
];

const capRows: ReadonlyArray<readonly [string, keyof PipelineSettings['caps']]> = [
  ['Max discoveries / day', 'maxDiscoveriesPerDay'],
  ['Max audits / day', 'maxAuditsPerDay'],
  ['Max previews / day', 'maxPreviewsPerDay'],
  ['Max emails / day', 'maxEmailsPerDay'],
  ['Max review queue size', 'maxReviewQueueSize'],
  ['Max backlog size', 'maxBacklogSize'],
];

const budgetRows: ReadonlyArray<readonly [string, keyof PipelineSettings['budgets']]> = [
  ['Daily Bedrock USD', 'dailyBedrockUsd'],
  ['Daily Places USD', 'dailyPlacesUsd'],
  ['Daily email USD', 'dailyEmailUsd'],
];

const thresholdRows: ReadonlyArray<
  readonly [string, keyof PipelineSettings['thresholds'], number, number]
> = [
  ['Min technical-issue score', 'minTechnicalIssueScore', 100, 1],
  ['Min qualification score', 'minQualificationScore', 100, 1],
  ['Min contact confidence', 'minContactConfidence', 1, 0.01],
];

// reasonKey converts a StageFlags key into the corresponding
// StagePauseReasons key — `auditEnabled` → `audit`.
function reasonKey(stageKey: keyof PipelineSettings['stages']): keyof StagePauseReasonsLite {
  return stageKey.replace(/Enabled$/, '') as keyof StagePauseReasonsLite;
}
type StagePauseReasonsLite = NonNullable<PipelineSettings['stagePauseReasons']>;

const section = { marginTop: '1.25rem' } as const;
const h3 = { margin: '0 0 0.5rem', fontSize: '1rem' } as const;
const inlineLabel = {
  display: 'flex',
  gap: '0.5rem',
  alignItems: 'center',
  marginBottom: '0.25rem',
} as const;
const blockLabel = { display: 'block', marginBottom: '0.25rem' } as const;
const numInput = { width: 110, padding: '0.2rem 0.4rem' } as const;
const hint = { color: '#666', margin: '0 0 0.5rem' } as const;
const saveButton = {
  padding: '0.5rem 1rem',
  background: '#0a3',
  color: 'white',
  border: 'none',
  borderRadius: 4,
  cursor: 'pointer',
} as const;
