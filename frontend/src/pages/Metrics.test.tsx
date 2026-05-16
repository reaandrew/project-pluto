import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import Metrics, { orderedDays, verticalRows } from './Metrics';
import type { MetricDay } from '../api';

const sample = {
  recent: [
    {
      id: 'id-1',
      name: 'Acme',
      domain: 'acme.co.uk',
      vertical: 'accountants',
      location: 'Manchester',
      source: 'csv',
      confidence: 1,
      status: 'new',
      createdAt: '2026-05-11T10:00:00Z',
    },
    {
      id: 'id-2',
      name: 'Beta',
      domain: 'beta.co.uk',
      vertical: 'accountants',
      location: 'Leeds',
      source: 'companies-house',
      confidence: 0.85,
      status: 'new',
      createdAt: '2026-05-10T08:00:00Z',
    },
  ],
  countsByDay: {
    '2026-05-11': 1,
    '2026-05-10': 1,
    '2026-05-09': 0,
    '2026-05-08': 0,
    '2026-05-07': 0,
    '2026-05-06': 0,
    '2026-05-05': 0,
  },
  totalLast7Day: 2,
};

beforeEach(() => {
  (window as unknown as { __FINANCE_CONFIG__: object }).__FINANCE_CONFIG__ = {
    bffBaseUrl: 'https://test-bff.example.com',
    apiBaseUrl: 'https://test-api.example.com',
    environment: 'unit-test',
    gitSha: 'x',
  };
});

function stubFetch(map: Record<string, (init?: RequestInit) => Response>) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const handler = Object.entries(map).find(([k]) => url.endsWith(k))?.[1];
      if (handler) return Promise.resolve(handler(init));
      // The iter-11.2/11.3 dashboard fires /metrics/rollup on mount;
      // default it to an empty window unless a test overrides it, so
      // the existing discoveries-widget tests stay focused.
      if (url.endsWith('/metrics/rollup')) {
        return Promise.resolve(json(200, { from: '', to: '', days: [] }));
      }
      throw new Error(`Unstubbed fetch: ${url}`);
    })
  );
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('Metrics', () => {
  it('renders loading then the discoveries widget', async () => {
    stubFetch({ '/metrics/discoveries': () => json(200, sample) });
    render(<Metrics />);
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    await waitFor(() => {
      expect(
        screen.getByRole('heading', { name: /Discoveries — last 7 days/i })
      ).toBeInTheDocument();
    });
    expect(screen.getByText('Acme')).toBeInTheDocument();
    expect(screen.getByText('Beta')).toBeInTheDocument();
  });

  it('renders the total + per-day breakdown', async () => {
    stubFetch({ '/metrics/discoveries': () => json(200, sample) });
    render(<Metrics />);
    await waitFor(() => screen.getByRole('heading', { name: /Discoveries/ }));
    // Total surfaces as bold "2".
    expect(screen.getByText('2')).toBeInTheDocument();
    // 7 day rows in the breakdown.
    expect(screen.getByText('2026-05-11:')).toBeInTheDocument();
    expect(screen.getByText('2026-05-05:')).toBeInTheDocument();
  });

  it('renders the empty state when no recent discoveries', async () => {
    stubFetch({
      '/metrics/discoveries': () => json(200, { recent: [], countsByDay: {}, totalLast7Day: 0 }),
    });
    render(<Metrics />);
    await waitFor(() => {
      expect(screen.getByText(/No discoveries yet/i)).toBeInTheDocument();
    });
  });

  it('"Run discovery now" POSTs then refreshes', async () => {
    let runCalled = 0;
    let listCalled = 0;
    stubFetch({
      '/metrics/discoveries/run': () => {
        runCalled++;
        return json(202, { status: 'ok', startedAt: '2026-05-11T12:00:00Z' });
      },
      '/metrics/discoveries': () => {
        listCalled++;
        return json(200, sample);
      },
    });
    render(<Metrics />);
    await waitFor(() => screen.getByRole('heading', { name: /Discoveries/ }));
    fireEvent.click(screen.getByRole('button', { name: /run-discovery-now/i }));
    await waitFor(() => {
      expect(screen.getByText(/Discovery run started/i)).toBeInTheDocument();
    });
    expect(runCalled).toBe(1);
    // Initial list call + refresh-after-run call.
    expect(listCalled).toBeGreaterThanOrEqual(2);
  });

  it('shows a Run failed message on 502', async () => {
    stubFetch({
      '/metrics/discoveries/run': () => new Response('bad gateway', { status: 502 }),
      '/metrics/discoveries': () => json(200, sample),
    });
    render(<Metrics />);
    await waitFor(() => screen.getByRole('heading', { name: /Discoveries/ }));
    fireEvent.click(screen.getByRole('button', { name: /run-discovery-now/i }));
    await waitFor(() => {
      expect(screen.getByText(/Run failed/i)).toBeInTheDocument();
    });
  });

  it('shows an error message when GET fails', async () => {
    stubFetch({
      '/metrics/discoveries': () => new Response('boom', { status: 500 }),
    });
    render(<Metrics />);
    await waitFor(() => {
      expect(screen.getByText(/Could not load discoveries/i)).toBeInTheDocument();
    });
  });
});

describe('orderedDays', () => {
  it('sorts dates descending so today is first', () => {
    const out = orderedDays({
      '2026-05-09': 1,
      '2026-05-11': 3,
      '2026-05-10': 2,
    });
    expect(out.map((p) => p[0])).toEqual(['2026-05-11', '2026-05-10', '2026-05-09']);
  });
});

const rollupDay: MetricDay = {
  date: '2026-05-16',
  funnel: { new: 9, emailed: 10, responded: 4, converted: 1 },
  perVertical: {
    accountants: {
      funnel: { emailed: 6, responded: 3, converted: 1 },
      styleVersion: 4,
      toneVersion: 2,
    },
    dentist: {
      funnel: { emailed: 4, responded: 1, converted: 0 },
      styleVersion: 1,
      toneVersion: 1,
    },
  },
  costByStage: { audit: 1.5, outreach: 0.25 },
  totalCostUsd: 1.75,
  generatedAt: '2026-05-16T09:00:00Z',
};

describe('verticalRows', () => {
  it('computes reply/conversion rates and sorts by reply rate desc', () => {
    const rows = verticalRows(rollupDay);
    expect(rows.map((r) => r.vertical)).toEqual(['accountants', 'dentist']);
    expect(rows[0].replyRate).toBeCloseTo(0.5); // 3/6
    expect(rows[0].conversionRate).toBeCloseTo(1 / 6);
    expect(rows[1].replyRate).toBeCloseTo(0.25); // 1/4
    expect(rows[0].styleVersion).toBe(4);
  });
  it('is empty + division-safe with no data', () => {
    expect(verticalRows(undefined)).toEqual([]);
  });
});

describe('Metrics dashboard', () => {
  it('renders funnel, cost, and the vertical comparison sorted by reply rate', async () => {
    stubFetch({
      '/metrics/discoveries': () => json(200, sample),
      '/metrics/rollup': () =>
        json(200, { from: '2026-05-16', to: '2026-05-16', days: [rollupDay] }),
    });
    render(<Metrics />);
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /Funnel — 2026-05-16/ })).toBeInTheDocument()
    );
    expect(
      screen.getByRole('heading', { name: /Cost — 2026-05-16 to 2026-05-16/ })
    ).toBeInTheDocument();
    expect(screen.getByText(/\$1\.75/)).toBeInTheDocument();
    expect(
      screen.getByRole('heading', { name: /Vertical comparison — sorted by reply rate/ })
    ).toBeInTheDocument();
    const rowsText = screen.getAllByRole('row').map((r) => r.textContent ?? '');
    const acc = rowsText.findIndex((t) => t.includes('accountants'));
    const den = rowsText.findIndex((t) => t.includes('dentist'));
    expect(acc).toBeGreaterThan(-1);
    expect(acc).toBeLessThan(den); // accountants (50%) above dentist (25%)
  });
});
