import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Replies from './Replies';
import type { RepliesResponse } from '../api';

function renderReplies() {
  return render(
    <MemoryRouter initialEntries={['/replies']}>
      <Replies />
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

function repliesPage(overrides: Partial<RepliesResponse> = {}): RepliesResponse {
  return {
    status: 'operator_inbox',
    items: [
      {
        ref: 'cmVm',
        id: 't-1',
        businessId: 'biz-1',
        category: 'unknown',
        confidence: 0.42,
        rationale: 'ambiguous one-liner',
        excerpt: 'who is this exactly?',
        triageState: 'operator_inbox',
        createdAt: '2026-05-16T12:00:00Z',
      },
    ],
    ...overrides,
  };
}

describe('Replies', () => {
  it('renders the inbox with category + confidence + excerpt', async () => {
    stubFetch([
      (u, i) => (u.includes('/replies') && (!i || !i.method) ? json(200, repliesPage()) : null),
    ]);
    renderReplies();
    await waitFor(() => expect(screen.getByText('who is this exactly?')).toBeInTheDocument());
    expect(screen.getByText('unknown', { selector: 'strong' })).toBeInTheDocument();
    expect(screen.getByText(/42%/)).toBeInTheDocument();
    expect(screen.getByText(/ambiguous one-liner/)).toBeInTheDocument();
  });

  it('reclassifies an item and drops it from the list', async () => {
    const calls: Array<{ url: string; body: unknown }> = [];
    stubFetch([
      (u, i) => {
        if (u.includes('/reclassify') && i?.method === 'POST') {
          calls.push({ url: u, body: JSON.parse(String(i.body)) });
          return json(200, { status: 'ok' });
        }
        if (u.includes('/replies') && (!i || !i.method)) return json(200, repliesPage());
        return null;
      },
    ]);
    renderReplies();
    await waitFor(() => expect(screen.getByText('who is this exactly?')).toBeInTheDocument());

    fireEvent.change(screen.getByLabelText('reclassify t-1'), {
      target: { value: 'unsubscribe' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));

    await waitFor(() => expect(screen.queryByText('who is this exactly?')).not.toBeInTheDocument());
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toContain('/replies/t-1/reclassify');
    expect(calls[0].body).toMatchObject({ ref: 'cmVm', newCategory: 'unsubscribe' });
  });

  it('shows an error banner when the list call fails', async () => {
    stubFetch([(u) => (u.includes('/replies') ? json(500, 'boom') : null)]);
    renderReplies();
    await waitFor(() => expect(screen.getByText(/Error:/)).toBeInTheDocument());
  });

  it('shows the empty state', async () => {
    stubFetch([(u) => (u.includes('/replies') ? json(200, repliesPage({ items: [] })) : null)]);
    renderReplies();
    await waitFor(() => expect(screen.getByText('No replies to review.')).toBeInTheDocument());
  });
});
