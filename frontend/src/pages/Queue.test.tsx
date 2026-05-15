import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Queue from './Queue';
import type { QueueResponse } from '../api';

function renderQueue() {
  return render(
    <MemoryRouter initialEntries={['/queue']}>
      <Queue />
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

function queuePage(overrides: Partial<QueueResponse> = {}): QueueResponse {
  return {
    status: 'awaiting_review',
    items: [
      {
        id: 'biz-1',
        name: 'Acme Plumbing',
        domain: 'acme.co.uk',
        vertical: 'trades',
        location: 'Manchester',
        status: 'awaiting_review',
        priorityScore: 0.86,
        lastWebsiteId: 'web-1',
      },
      {
        id: 'biz-2',
        name: 'Beta Dental',
        domain: 'beta.co.uk',
        vertical: 'dentist',
        location: 'Leeds',
        status: 'awaiting_review',
        priorityScore: 0.42,
        lastWebsiteId: 'web-2',
      },
    ],
    ...overrides,
  };
}

const settings = { caps: { maxReviewQueueSize: 25 } };

function baseHandlers(): Array<(u: string, i?: RequestInit) => Response | null> {
  return [
    (u, i) =>
      u.includes('/queue') && (!i || i.method === undefined) ? json(200, queuePage()) : null,
    (u) => (u.endsWith('/settings') ? json(200, settings) : null),
    (u) => (u.includes('/website') ? json(200, { business: {}, website: null }) : null),
  ];
}

describe('Queue', () => {
  it('renders priority-ordered cards + the daily-cap line', async () => {
    stubFetch(baseHandlers());
    renderQueue();
    await waitFor(() => expect(screen.getByText('Acme Plumbing')).toBeInTheDocument());
    expect(screen.getByText('Beta Dental')).toBeInTheDocument();
    expect(screen.getByText(/priority 0\.86/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/queue cap 25/)).toBeInTheDocument());
    expect(screen.getByTestId('reviewed-count')).toHaveTextContent('0');
    expect(screen.getByText(/reviewed this session/)).toBeInTheDocument();
  });

  it('filters by vertical', async () => {
    stubFetch(baseHandlers());
    renderQueue();
    await waitFor(() => expect(screen.getByText('Acme Plumbing')).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText('filter-vertical'), { target: { value: 'dentist' } });
    expect(screen.queryByText('Acme Plumbing')).not.toBeInTheDocument();
    expect(screen.getByText('Beta Dental')).toBeInTheDocument();
  });

  it('approve removes the card and bumps the session counter', async () => {
    stubFetch([
      (u, i) =>
        u.endsWith('/website/web-1/approve') && i?.method === 'POST'
          ? json(200, { id: 'web-1', status: 'approved' })
          : null,
      ...baseHandlers(),
    ]);
    renderQueue();
    await waitFor(() => expect(screen.getByText('Acme Plumbing')).toBeInTheDocument());
    const approveBtns = screen.getAllByRole('button', { name: 'Approve' });
    fireEvent.click(approveBtns[0]);
    await waitFor(() => expect(screen.queryByText('Acme Plumbing')).not.toBeInTheDocument());
    expect(screen.getByTestId('reviewed-count')).toHaveTextContent('1');
    expect(screen.getByText('Beta Dental')).toBeInTheDocument();
  });

  it('reject requires a reason then resolves the card', async () => {
    let rejectBody = '';
    stubFetch([
      (u, i) => {
        if (u.endsWith('/website/web-1/reject') && i?.method === 'POST') {
          rejectBody = String(i?.body ?? '');
          return json(200, { id: 'web-1', status: 'rejected' });
        }
        return null;
      },
      ...baseHandlers(),
    ]);
    renderQueue();
    await waitFor(() => expect(screen.getByText('Acme Plumbing')).toBeInTheDocument());
    fireEvent.click(screen.getAllByRole('button', { name: 'Reject…' })[0]);
    fireEvent.change(screen.getAllByLabelText('reject-reason')[0], {
      target: { value: 'too_small' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Confirm reject' }));
    await waitFor(() => expect(screen.queryByText('Acme Plumbing')).not.toBeInTheDocument());
    expect(rejectBody).toContain('too_small');
  });

  it('shows an error when the queue fails to load', async () => {
    stubFetch([(u) => (u.includes('/queue') ? json(500, { error: 'boom' }) : null)]);
    renderQueue();
    await waitFor(() => expect(screen.getByText(/Could not load queue/)).toBeInTheDocument());
  });
});
