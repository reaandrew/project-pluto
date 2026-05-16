import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Feedback from './Feedback';
import type { FeedbackResponse } from '../api';

function renderFeedback() {
  return render(
    <MemoryRouter initialEntries={['/feedback']}>
      <Feedback />
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

function page(overrides: Partial<FeedbackResponse> = {}): FeedbackResponse {
  return {
    vertical: 'default',
    items: [
      {
        id: 'f-1',
        subject: 'email',
        subjectId: 'draft-1',
        businessId: 'biz-1',
        actor: 'cog-1',
        action: 'edit',
        notes: 'tightened the subject line',
        vertical: 'default',
        createdAt: '2026-05-16T12:00:00Z',
      },
    ],
    ...overrides,
  };
}

describe('Feedback', () => {
  it('renders the log rows', async () => {
    stubFetch([(u) => (u.includes('/feedback') ? json(200, page()) : null)]);
    renderFeedback();
    await waitFor(() => expect(screen.getByText('draft-1')).toBeInTheDocument());
    expect(screen.getByText('tightened the subject line')).toBeInTheDocument();
    expect(screen.getByText('edit')).toBeInTheDocument();
  });

  it('passes the stage filter through to the BFF', async () => {
    const urls: string[] = [];
    stubFetch([
      (u) => {
        urls.push(u);
        return u.includes('/feedback') ? json(200, page({ items: [] })) : null;
      },
    ]);
    renderFeedback();
    await waitFor(() =>
      expect(screen.getByText('No feedback for these filters.')).toBeInTheDocument()
    );
    fireEvent.change(screen.getByLabelText('stage'), { target: { value: 'spec' } });
    await waitFor(() => expect(urls.some((u) => u.includes('subject=spec'))).toBe(true));
  });

  it('shows an error banner on failure', async () => {
    stubFetch([(u) => (u.includes('/feedback') ? json(500, 'boom') : null)]);
    renderFeedback();
    await waitFor(() => expect(screen.getByText(/Error:/)).toBeInTheDocument());
  });
});
