import { useEffect, useState } from 'react';
import {
  getDiscoveries,
  getMetricsRollup,
  runDiscoveryNow,
  type DiscoveriesResponse,
  type DiscoveryRow,
  type MetricDay,
} from '../api';

const FUNNEL_ORDER = [
  'new',
  'auditing',
  'qualified',
  'rejected',
  'awaiting_review',
  'approved',
  'regenerate_requested',
  'rejected_after_review',
  'email_drafted',
  'emailed',
  'responded',
  'converted',
] as const;

export interface VerticalRow {
  vertical: string;
  emailed: number;
  responded: number;
  converted: number;
  replyRate: number;
  conversionRate: number;
  styleVersion: number;
  toneVersion: number;
}

const rate = (num: number, denom: number): number => (denom > 0 ? num / denom : 0);

// verticalRows derives the per-vertical comparison from the latest
// day's snapshot, sorted by reply rate descending. Exported for tests.
export function verticalRows(latest: MetricDay | undefined): VerticalRow[] {
  if (!latest) return [];
  return Object.entries(latest.perVertical)
    .map(([vertical, vm]) => {
      const emailed = vm.funnel.emailed ?? 0;
      const responded = vm.funnel.responded ?? 0;
      const converted = vm.funnel.converted ?? 0;
      return {
        vertical,
        emailed,
        responded,
        converted,
        replyRate: rate(responded, emailed),
        conversionRate: rate(converted, emailed),
        styleVersion: vm.styleVersion,
        toneVersion: vm.toneVersion,
      };
    })
    .sort((a, b) => b.replyRate - a.replyRate);
}

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
  const [rollup, setRollup] = useState<MetricDay[] | null>(null);

  function refresh() {
    getDiscoveries()
      .then((d) => {
        setData(d);
        setLoadError(null);
      })
      .catch((err: Error) => setLoadError(err.message));
    // The funnel/cost dashboard is best-effort: a roll-up gap must not
    // blank the discoveries widget.
    getMetricsRollup()
      .then((r) => setRollup(r.days))
      .catch(() => setRollup([]));
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

      <MetricsDashboard days={rollup} />
    </div>
  );
}

function MetricsDashboard({ days }: { days: MetricDay[] | null }) {
  if (days === null) {
    return (
      <section style={section}>
        <h3 style={h3}>Funnel &amp; cost</h3>
        <p>Loading…</p>
      </section>
    );
  }
  if (days.length === 0) {
    return (
      <section style={section}>
        <h3 style={h3}>Funnel &amp; cost</h3>
        <p style={{ color: '#666' }}>
          No roll-up yet — the hourly metrics-rollup writes the first snapshot shortly.
        </p>
      </section>
    );
  }
  const latest = days[days.length - 1];
  const windowCost = days.reduce((s, d) => s + d.totalCostUsd, 0);
  const costByStage: Record<string, number> = {};
  for (const d of days) {
    for (const [st, v] of Object.entries(d.costByStage ?? {})) {
      costByStage[st] = (costByStage[st] ?? 0) + v;
    }
  }
  const vrows = verticalRows(latest);

  return (
    <>
      <section style={section}>
        <h3 style={h3}>Funnel — {latest.date}</h3>
        <ul style={{ listStyle: 'none', padding: 0, margin: '0.5rem 0' }}>
          {FUNNEL_ORDER.map((s) => (
            <li key={s} style={{ fontFamily: 'monospace' }}>
              {s.padEnd(22)}: <strong>{latest.funnel?.[s] ?? 0}</strong>
              <span style={{ color: '#aaa', marginLeft: '0.5rem' }}>
                {bar(latest.funnel?.[s] ?? 0)}
              </span>
            </li>
          ))}
        </ul>
      </section>

      <section style={section}>
        <h3 style={h3}>
          Cost — {days[0].date} to {latest.date}
        </h3>
        <p style={{ color: '#666' }}>
          Total spend: <strong>${windowCost.toFixed(2)}</strong> over {days.length} day(s).
        </p>
        <ul style={{ listStyle: 'none', padding: 0, margin: '0.5rem 0' }}>
          {Object.entries(costByStage)
            .sort((a, b) => b[1] - a[1])
            .map(([st, v]) => (
              <li key={st} style={{ fontFamily: 'monospace' }}>
                {st.padEnd(14)}: <strong>${v.toFixed(2)}</strong>
              </li>
            ))}
        </ul>
      </section>

      <section style={section}>
        <h3 style={h3}>Vertical comparison — sorted by reply rate</h3>
        {vrows.length === 0 ? (
          <p style={{ color: '#666' }}>No per-vertical data yet.</p>
        ) : (
          <table style={{ borderCollapse: 'collapse', width: '100%' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid #ddd', textAlign: 'left' }}>
                <th style={th}>Vertical</th>
                <th style={th}>Emailed</th>
                <th style={th}>Replied</th>
                <th style={th}>Reply rate</th>
                <th style={th}>Conv. rate</th>
                <th style={th}>Style v</th>
                <th style={th}>Tone v</th>
              </tr>
            </thead>
            <tbody>
              {vrows.map((r) => (
                <tr key={r.vertical} style={{ borderBottom: '1px solid #eee' }}>
                  <td style={td}>{r.vertical}</td>
                  <td style={td}>{r.emailed}</td>
                  <td style={td}>{r.responded}</td>
                  <td style={td}>{(r.replyRate * 100).toFixed(1)}%</td>
                  <td style={td}>{(r.conversionRate * 100).toFixed(1)}%</td>
                  <td style={td}>{r.styleVersion}</td>
                  <td style={td}>{r.toneVersion}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p style={{ color: '#888', fontSize: '0.85rem', marginTop: '0.5rem' }}>
          Style/Tone v columns tag each vertical with the profile version in effect — compare reply
          rate across days to see whether an applied tuner delta moved the numbers.
        </p>
      </section>
    </>
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
