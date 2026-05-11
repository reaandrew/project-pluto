import { useEffect, useState } from 'react';
import {
  createTargetingProfile,
  deleteTargetingProfile,
  listTargetingProfiles,
  updateTargetingProfile,
  type TargetingProfile,
  type TargetingWeights,
} from '../api';

// /settings/targeting renders the list of TargetingProfile rows and a
// form to edit or create them. Lifecycle:
//
//   list → click row → editor (controlled state) → Save (PATCH with
//   If-Match etag) → list refresh.
//   "New profile" → editor pre-filled with a blank template → Save
//   (POST) → list refresh.
//   "Delete" on a row → DELETE → list refresh.
//
// Etag-mismatch responses surface as a save-failed message; the
// operator clicks the row again to reload the current version and
// retry their edit.
export default function Targeting() {
  const [profiles, setProfiles] = useState<TargetingProfile[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<TargetingProfile | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [saveStatus, setSaveStatus] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');
  const [saveError, setSaveError] = useState<string | null>(null);

  function refresh() {
    listTargetingProfiles()
      .then((ps) => {
        setProfiles(ps);
        setLoadError(null);
      })
      .catch((err: Error) => setLoadError(err.message));
  }

  useEffect(refresh, []);

  function openNew() {
    setEditing(blankProfile());
    setIsNew(true);
    setSaveStatus('idle');
    setSaveError(null);
  }

  function openExisting(p: TargetingProfile) {
    setEditing(p);
    setIsNew(false);
    setSaveStatus('idle');
    setSaveError(null);
  }

  async function onSave() {
    if (!editing) return;
    setSaveStatus('saving');
    setSaveError(null);
    try {
      if (isNew) {
        await createTargetingProfile({
          vertical: editing.vertical,
          location: editing.location,
          includeKeywords: editing.includeKeywords,
          excludeKeywords: editing.excludeKeywords,
          weights: editing.weights,
          enabled: editing.enabled,
        });
      } else {
        await updateTargetingProfile(editing.id, editing);
      }
      setSaveStatus('saved');
      setEditing(null);
      refresh();
    } catch (err) {
      setSaveStatus('error');
      setSaveError((err as Error).message);
    }
  }

  async function onDelete(p: TargetingProfile) {
    if (!confirm(`Delete profile "${p.vertical} — ${p.location}"?`)) return;
    try {
      await deleteTargetingProfile(p.id);
      refresh();
    } catch (err) {
      setLoadError((err as Error).message);
    }
  }

  if (loadError && !editing) {
    return (
      <div>
        <h2>Targeting profiles</h2>
        <p style={{ color: '#b00' }}>{loadError}</p>
      </div>
    );
  }
  if (profiles === null) {
    return (
      <div>
        <h2>Targeting profiles</h2>
        <p>Loading…</p>
      </div>
    );
  }

  if (editing) {
    return (
      <ProfileEditor
        profile={editing}
        isNew={isNew}
        saveStatus={saveStatus}
        saveError={saveError}
        onChange={setEditing}
        onSave={onSave}
        onCancel={() => setEditing(null)}
      />
    );
  }

  return (
    <div>
      <h2>Targeting profiles</h2>
      <p style={{ color: '#666' }}>
        Each profile drives one Discovery loop — vertical + location + keywords + scoring weights.
      </p>
      <button type="button" style={primary} onClick={openNew}>
        New profile
      </button>
      {profiles.length === 0 ? (
        <p style={{ color: '#666', marginTop: '1rem' }}>
          No profiles yet. Click "New profile" to add the first one.
        </p>
      ) : (
        <table style={{ marginTop: '1rem', borderCollapse: 'collapse', width: '100%' }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #ddd', textAlign: 'left' }}>
              <th style={th}>Vertical</th>
              <th style={th}>Location</th>
              <th style={th}>Enabled</th>
              <th style={th}>Discovered 7d</th>
              <th style={th}>Qualified 7d</th>
              <th style={th}></th>
            </tr>
          </thead>
          <tbody>
            {profiles.map((p) => (
              <tr key={p.id} style={{ borderBottom: '1px solid #eee' }}>
                <td style={td}>
                  <button type="button" style={linkButton} onClick={() => openExisting(p)}>
                    {p.vertical}
                  </button>
                </td>
                <td style={td}>{p.location}</td>
                <td style={td}>{p.enabled ? 'yes' : 'no'}</td>
                <td style={td}>{p.stats?.discovered7d ?? 0}</td>
                <td style={td}>{p.stats?.qualified7d ?? 0}</td>
                <td style={td}>
                  <button type="button" style={dangerLink} onClick={() => onDelete(p)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

interface ProfileEditorProps {
  profile: TargetingProfile;
  isNew: boolean;
  saveStatus: 'idle' | 'saving' | 'saved' | 'error';
  saveError: string | null;
  onChange: (p: TargetingProfile) => void;
  onSave: () => void;
  onCancel: () => void;
}

function ProfileEditor({
  profile,
  isNew,
  saveStatus,
  saveError,
  onChange,
  onSave,
  onCancel,
}: ProfileEditorProps) {
  function setField<K extends keyof TargetingProfile>(key: K, value: TargetingProfile[K]) {
    onChange({ ...profile, [key]: value });
  }
  function setWeight(key: keyof TargetingWeights, value: number) {
    onChange({ ...profile, weights: { ...profile.weights, [key]: value } });
  }

  const weightSum =
    profile.weights.websiteAge +
    profile.weights.auditScore +
    profile.weights.businessSize +
    profile.weights.contactConfidence +
    profile.weights.verticalFit;
  const weightsValid = weightSum >= 0.99 && weightSum <= 1.01;

  return (
    <div>
      <h2>{isNew ? 'New targeting profile' : `Edit: ${profile.vertical}`}</h2>

      <label style={blockLabel}>
        <span style={fieldLabel}>Vertical</span>
        <input
          type="text"
          value={profile.vertical}
          onChange={(e) => setField('vertical', e.target.value)}
          style={textInput}
        />
      </label>

      <label style={blockLabel}>
        <span style={fieldLabel}>Location</span>
        <input
          type="text"
          value={profile.location}
          onChange={(e) => setField('location', e.target.value)}
          style={textInput}
        />
      </label>

      <label style={blockLabel}>
        <span style={fieldLabel}>Include keywords (comma-separated)</span>
        <input
          type="text"
          value={profile.includeKeywords.join(', ')}
          onChange={(e) => setField('includeKeywords', splitKeywords(e.target.value))}
          style={textInput}
        />
      </label>

      <label style={blockLabel}>
        <span style={fieldLabel}>Exclude keywords (comma-separated)</span>
        <input
          type="text"
          value={profile.excludeKeywords.join(', ')}
          onChange={(e) => setField('excludeKeywords', splitKeywords(e.target.value))}
          style={textInput}
        />
      </label>

      <h3 style={h3}>Weights (must sum to 1.0)</h3>
      {weightRows.map(([label, key]) => (
        <label key={key} style={blockLabel}>
          <span style={fieldLabel}>{label}</span>
          <input
            type="number"
            min={0}
            max={1}
            step={0.05}
            value={profile.weights[key]}
            onChange={(e) => setWeight(key, Number(e.target.value))}
            style={numInput}
          />
        </label>
      ))}
      <p style={{ color: weightsValid ? '#070' : '#b00' }} aria-label="weight-sum">
        Sum: {weightSum.toFixed(2)}
        {weightsValid ? ' ✓' : ' (must be 1.0)'}
      </p>

      <label style={inlineLabel}>
        <input
          type="checkbox"
          checked={profile.enabled}
          onChange={(e) => setField('enabled', e.target.checked)}
        />
        <span>Enabled</span>
      </label>

      <div style={{ marginTop: '1.5rem' }}>
        <button
          type="button"
          style={primary}
          disabled={saveStatus === 'saving' || !weightsValid}
          onClick={onSave}
        >
          {saveStatus === 'saving' ? 'Saving…' : 'Save'}
        </button>
        <button type="button" style={secondary} onClick={onCancel}>
          Cancel
        </button>
        {saveStatus === 'saved' && (
          <span style={{ marginLeft: '1rem', color: '#070' }}>Saved.</span>
        )}
        {saveStatus === 'error' && saveError && (
          <span style={{ marginLeft: '1rem', color: '#b00' }}>Save failed: {saveError}</span>
        )}
      </div>
    </div>
  );
}

function blankProfile(): TargetingProfile {
  return {
    id: '',
    vertical: '',
    location: '',
    includeKeywords: [],
    excludeKeywords: [],
    weights: {
      websiteAge: 0.2,
      auditScore: 0.3,
      businessSize: 0.2,
      contactConfidence: 0.2,
      verticalFit: 0.1,
    },
    enabled: true,
    stats: { discovered7d: 0, qualified7d: 0, approved7d: 0 },
    createdAt: '',
    updatedAt: '',
    etag: '',
  };
}

function splitKeywords(s: string): string[] {
  return s
    .split(',')
    .map((p) => p.trim())
    .filter(Boolean);
}

const weightRows: ReadonlyArray<readonly [string, keyof TargetingWeights]> = [
  ['Website age', 'websiteAge'],
  ['Audit score', 'auditScore'],
  ['Business size', 'businessSize'],
  ['Contact confidence', 'contactConfidence'],
  ['Vertical fit', 'verticalFit'],
];

const th = { padding: '0.4rem 0.5rem', color: '#666', fontWeight: 600 } as const;
const td = { padding: '0.4rem 0.5rem' } as const;
const blockLabel = { display: 'block', marginBottom: '0.5rem' } as const;
const inlineLabel = {
  display: 'flex',
  gap: '0.5rem',
  alignItems: 'center',
  marginTop: '1rem',
} as const;
const fieldLabel = { display: 'inline-block', width: 220, color: '#666' } as const;
const textInput = { padding: '0.3rem 0.5rem', width: 320 } as const;
const numInput = { width: 110, padding: '0.2rem 0.4rem' } as const;
const h3 = { marginTop: '1.25rem', fontSize: '1rem' } as const;
const primary = {
  padding: '0.5rem 1rem',
  background: '#0a3',
  color: 'white',
  border: 'none',
  borderRadius: 4,
  cursor: 'pointer',
} as const;
const secondary = {
  marginLeft: '0.5rem',
  padding: '0.5rem 1rem',
  background: '#fff',
  border: '1px solid #ccc',
  borderRadius: 4,
  cursor: 'pointer',
} as const;
const linkButton = {
  background: 'none',
  border: 'none',
  color: '#06c',
  cursor: 'pointer',
  padding: 0,
  font: 'inherit',
  textDecoration: 'underline',
} as const;
const dangerLink = {
  background: 'none',
  border: 'none',
  color: '#b00',
  cursor: 'pointer',
  padding: 0,
  font: 'inherit',
} as const;
