import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import Metrics, { orderedDays } from './Metrics';

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
      if (!handler) throw new Error(`Unstubbed fetch: ${url}`);
      return Promise.resolve(handler(init));
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
