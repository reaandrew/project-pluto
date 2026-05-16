import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import EmailReview from './EmailReview';
import type { CandidateEmailResponse } from '../api';

function renderEmail(businessId = 'biz-1') {
  return render(
    <MemoryRouter initialEntries={[`/queue/${businessId}/email`]}>
      <Routes>
        <Route path="/queue/:businessId/email" element={<EmailReview />} />
      </Routes>
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

function fakeEmail(overrides = {}): CandidateEmailResponse {
  return {
    business: {
      id: 'biz-1',
      name: 'Acme Accountants',
      domain: 'acme.co.uk',
      vertical: 'accountants',
      location: 'Manchester',
      status: 'approved',
    },
    email: {
      id: 'draft-1',
      websiteId: 'web-1',
      contactId: 'con-1',
      subject: 'Quick redesign preview for Acme',
      body: "Hi Jane,\nPreview: https://p/sites/web-1\nUse access code H7Q32KX9.\nReply 'no thanks'.",
      optOutLine: "Reply 'no thanks'.",
      wordCount: 60,
      modelId: 'anthropic.claude-haiku-4-5',
      promptId: 'email.v1',
      status: 'draft',
      createdAt: '2026-05-16T11:00:00Z',
      updatedAt: '2026-05-16T11:00:00Z',
    },
    ...overrides,
  };
}

describe('EmailReview', () => {
  it('renders the draft + static checks', async () => {
    stubFetch([(u) => (u.endsWith('/candidates/biz-1/email') ? json(200, fakeEmail()) : null)]);
    renderEmail();
    await waitFor(() => expect(screen.getByText(/Email review/)).toBeInTheDocument());
    expect(screen.getByTestId('email-subject')).toHaveValue('Quick redesign preview for Acme');
    // Static checks panel: opt-out present + no "password" should pass.
    expect(screen.getByText(/opt-out line present/)).toBeInTheDocument();
    expect(screen.getByLabelText('static-checks')).toBeInTheDocument();
  });

  it('approve calls the BFF and reflects approved status', async () => {
    stubFetch([
      (u, i) =>
        u.endsWith('/email/draft-1/approve') && i?.method === 'POST'
          ? json(200, { ...fakeEmail().email, status: 'approved' })
          : null,
      (u) => (u.endsWith('/candidates/biz-1/email') ? json(200, fakeEmail()) : null),
    ]);
    renderEmail();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(screen.getByText(/Email is approved/)).toBeInTheDocument());
  });

  it('save edits calls PATCH with the edited body', async () => {
    let patched = '';
    stubFetch([
      (u, i) => {
        if (u.endsWith('/candidates/biz-1/email/draft-1') && i?.method === 'PATCH') {
          patched = String(i?.body ?? '');
          return json(200, { ...fakeEmail().email, subject: 'Edited subj' });
        }
        return null;
      },
      (u) => (u.endsWith('/candidates/biz-1/email') ? json(200, fakeEmail()) : null),
    ]);
    renderEmail();
    await waitFor(() => expect(screen.getByTestId('email-body')).toBeInTheDocument());
    fireEvent.focus(screen.getByTestId('email-body'));
    fireEvent.change(screen.getByTestId('email-body'), {
      target: { value: 'New copy with access code H7Q32KX9.' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save edits' }));
    await waitFor(() => expect(patched).toContain('New copy with access code'));
  });

  it('shows the empty state when no draft exists', async () => {
    stubFetch([
      (u) =>
        u.endsWith('/candidates/biz-1/email')
          ? json(200, { business: fakeEmail().business })
          : null,
    ]);
    renderEmail();
    await waitFor(() => expect(screen.getByText(/No email draft yet/)).toBeInTheDocument());
  });

  it('surfaces a load error', async () => {
    stubFetch([(u) => (u.includes('/email') ? json(500, { error: 'boom' }) : null)]);
    renderEmail();
    await waitFor(() => expect(screen.getByText(/Could not load email/)).toBeInTheDocument());
  });
});
