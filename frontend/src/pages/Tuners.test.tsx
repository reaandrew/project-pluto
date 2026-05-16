import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Tuners from './Tuners';
import type { TunersResponse } from '../api';

function renderTuners() {
  return render(
    <MemoryRouter initialEntries={['/tuners']}>
      <Tuners />
    </MemoryRouter>
  );
}

beforeEach(() => {
  (window as unknown as { __FINANCE_CONFIG__: object }).__FINANCE_CONFIG__ = {
    bffBaseUrl: 'https://test-bff.example.com',
    apiBaseUrl: 'https://test-api.example.com',
    environment: 'unit-test',
    gitSha: 'x',
  };
});

function stubFetch(handlers: Array<(url: string, init?: RequestInit) => Response | null>) {
  vi.stubGlobal(
    'fetch',
    vi.fn<(url: string, init?: RequestInit) => Promise<Response>>((url, init) => {
      for (const h of handlers) {
        const res = h(url, init);
        if (res) return Promise.resolve(res);
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

function page(overrides: Partial<TunersResponse> = {}): TunersResponse {
  return {
    status: 'pending',
    items: [
      {
        ref: 'cmVm',
        id: 'w1',
        kind: 'style',
        vertical: 'accountants',
        status: 'pending',
        rationale: 'operators removed the vague tagline 4/5 times',
        proposed: { addDoPhrases: ['lead with the fixed fee'] },
        promptId: 'tuner.style.v1',
        createdAt: '2026-05-16T02:00:00Z',
      },
    ],
    ...overrides,
  };
}

describe('Tuners', () => {
  it('renders a pending delta with rationale + proposed diff', async () => {
    stubFetch([(u) => (u.includes('/tuners') ? json(200, page()) : null)]);
    renderTuners();
    await waitFor(() =>
      expect(screen.getByText(/operators removed the vague tagline/)).toBeInTheDocument()
    );
    expect(screen.getByText('style', { selector: 'strong' })).toBeInTheDocument();
    expect(screen.getByText(/lead with the fixed fee/)).toBeInTheDocument();
  });

  it('applies a delta and drops it from the list', async () => {
    const calls: string[] = [];
    stubFetch([
      (u, i) => {
        if (u.includes('/apply') && i?.method === 'POST') {
          calls.push(u);
          return json(200, { status: 'ok' });
        }
        if (u.includes('/tuners') && (!i || !i.method)) return json(200, page());
        return null;
      },
    ]);
    renderTuners();
    await waitFor(() => expect(screen.getByText(/vague tagline/)).toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(screen.queryByText(/vague tagline/)).not.toBeInTheDocument());
    expect(calls[0]).toContain('/tuners/w1/apply');
  });

  it('shows an error banner on failure', async () => {
    stubFetch([(u) => (u.includes('/tuners') ? json(500, 'boom') : null)]);
    renderTuners();
    await waitFor(() => expect(screen.getByText(/Error:/)).toBeInTheDocument());
  });

  it('shows the empty state', async () => {
    stubFetch([(u) => (u.includes('/tuners') ? json(200, page({ items: [] })) : null)]);
    renderTuners();
    await waitFor(() =>
      expect(screen.getByText('No pending tuner proposals.')).toBeInTheDocument()
    );
  });
});
