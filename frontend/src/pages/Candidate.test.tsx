import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import Candidate from './Candidate';
import type { CandidateResponse, Spec } from '../api';

// Candidate is mounted under /queue/:businessId in main.tsx. The test
// wraps it with a MemoryRouter at the same path so useParams resolves.
function renderCandidate(businessId = 'biz-1') {
  return render(
    <MemoryRouter initialEntries={[`/queue/${businessId}`]}>
      <Routes>
        <Route path="/queue/:businessId" element={<Candidate />} />
      </Routes>
    </MemoryRouter>
  );
}

function fakeSpec(overrides: Partial<Spec> = {}): Spec {
  return {
    id: 'spec-1',
    version: 1,
    status: 'draft',
    modelId: 'anthropic.claude-sonnet-4-6',
    promptId: 'spec.v1',
    createdAt: '2026-05-14T12:00:00Z',
    updatedAt: '2026-05-14T12:00:00Z',
    etag: 'seed',
    content: {
      brand: {
        tone: 'plain',
        positioning: 'Local plumbers, Manchester.',
        palette: { primary: '#0F4C81', neutralDark: '#000', neutralLight: '#fff' },
      },
      page: {
        sections: [
          {
            type: 'hero',
            headline: 'Hi',
            subheadline: 'Hello',
            primaryCta: { label: 'Call', action: 'call' },
          },
          {
            type: 'services',
            title: 'What we do',
            items: [
              { name: 'a', oneLine: 'b' },
              { name: 'c', oneLine: 'd' },
              { name: 'e', oneLine: 'f' },
            ],
          },
          { type: 'about', paragraph: 'About us.' },
          { type: 'contact', phone: '0161 234 5678' },
        ],
      },
      seo: { title: 'Acme', description: 'Acme Plumbers.' },
      constraints: {
        doNotInventTestimonials: true,
        doNotInventAwards: true,
        doNotInventPrices: true,
      },
    },
    ...overrides,
  };
}

function fakeCandidate(overrides: Partial<CandidateResponse> = {}): CandidateResponse {
  return {
    business: {
      id: 'biz-1',
      name: 'Acme Plumbing',
      domain: 'acme.co.uk',
      vertical: 'trades',
      location: 'Manchester',
      status: 'qualified',
    },
    spec: fakeSpec(),
    ...overrides,
  };
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

describe('Candidate', () => {
  it('renders the business header + spec summary + JSON editor', async () => {
    stubFetch([(url) => (url.endsWith('/candidates/biz-1') ? json(200, fakeCandidate()) : null)]);
    renderCandidate();

    await waitFor(() => {
      expect(screen.getByText('Acme Plumbing')).toBeInTheDocument();
    });
    expect(screen.getByText(/acme\.co\.uk/)).toBeInTheDocument();
    expect(screen.getByText(/status: qualified/)).toBeInTheDocument();
    expect(screen.getByText(/v1 · draft · anthropic\.claude-sonnet-4-6/)).toBeInTheDocument();
    // Section trail rendered.
    expect(screen.getByText(/hero → services → about → contact/)).toBeInTheDocument();
    // Editor populated with the spec JSON.
    const editor = screen.getByTestId('spec-editor') as HTMLTextAreaElement;
    expect(editor.value).toContain('"tone": "plain"');
  });

  it('shows "no spec yet" when the BFF returns no spec', async () => {
    stubFetch([
      (url) =>
        url.endsWith('/candidates/biz-1') ? json(200, fakeCandidate({ spec: undefined })) : null,
    ]);
    renderCandidate();
    await waitFor(() => {
      expect(screen.getByText(/No spec generated for this business yet/i)).toBeInTheDocument();
    });
    expect(screen.queryByTestId('spec-editor')).not.toBeInTheDocument();
  });

  it('renders an error when GET fails', async () => {
    stubFetch([
      (url) => (url.endsWith('/candidates/biz-1') ? new Response('boom', { status: 500 }) : null),
    ]);
    renderCandidate();
    await waitFor(() => {
      expect(screen.getByText(/Could not load candidate/i)).toBeInTheDocument();
    });
  });

  it('Approve disables actions until the request resolves and replaces the spec', async () => {
    const updated = fakeSpec({ status: 'approved', approvedBy: 'cog-1' });
    stubFetch([
      (url) => (url.endsWith('/candidates/biz-1') ? json(200, fakeCandidate()) : null),
      (url, init) =>
        url.endsWith('/specs/spec-1/approve') && init?.method === 'POST'
          ? json(200, updated)
          : null,
    ]);
    renderCandidate();

    await waitFor(() => screen.getByRole('button', { name: 'Approve' }));
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }));

    await waitFor(() => {
      expect(screen.getByText(/Spec is approved\. Actions disabled\./i)).toBeInTheDocument();
    });
    expect((screen.getByRole('button', { name: 'Approve' }) as HTMLButtonElement).disabled).toBe(
      true
    );
  });

  it('Reject sends notes in the body', async () => {
    let postedBody = '';
    stubFetch([
      (url) => (url.endsWith('/candidates/biz-1') ? json(200, fakeCandidate()) : null),
      (url, init) => {
        if (!url.endsWith('/specs/spec-1/reject')) return null;
        postedBody = (init?.body as string) ?? '';
        return json(200, fakeSpec({ status: 'rejected' }));
      },
    ]);
    renderCandidate();
    await waitFor(() => screen.getByRole('button', { name: 'Reject' }));
    fireEvent.change(screen.getByTestId('spec-notes'), {
      target: { value: 'tone is too cold' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }));

    await waitFor(() => {
      expect(screen.getByText(/Spec is rejected/)).toBeInTheDocument();
    });
    expect(postedBody).toContain('tone is too cold');
  });

  it('Save edits sends the edited JSON body and refreshes the spec', async () => {
    const updated = fakeSpec({ version: 2 });
    updated.content.brand.tone = 'warmer, friendlier';
    let postedBody = '';
    stubFetch([
      (url) => (url.endsWith('/candidates/biz-1') ? json(200, fakeCandidate()) : null),
      (url, init) => {
        if (!(url.endsWith('/specs/spec-1') && init?.method === 'PATCH')) return null;
        postedBody = (init?.body as string) ?? '';
        return json(200, updated);
      },
    ]);
    renderCandidate();

    await waitFor(() => screen.getByTestId('spec-editor'));
    const editor = screen.getByTestId('spec-editor') as HTMLTextAreaElement;
    // Activate editing then change content.
    fireEvent.focus(editor);
    const edited = JSON.stringify(updated.content, null, 2);
    fireEvent.change(editor, { target: { value: edited } });
    fireEvent.click(screen.getByRole('button', { name: 'Save edits' }));

    await waitFor(() => {
      expect(screen.getByText(/v2 · draft/)).toBeInTheDocument();
    });
    expect(postedBody).toContain('warmer, friendlier');
  });

  it('Save edits surfaces a JSON parse error before sending', async () => {
    stubFetch([(url) => (url.endsWith('/candidates/biz-1') ? json(200, fakeCandidate()) : null)]);
    renderCandidate();

    await waitFor(() => screen.getByTestId('spec-editor'));
    const editor = screen.getByTestId('spec-editor') as HTMLTextAreaElement;
    fireEvent.focus(editor);
    fireEvent.change(editor, { target: { value: 'not-json' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save edits' }));

    await waitFor(() => {
      expect(screen.getByText(/JSON parse error:/i)).toBeInTheDocument();
    });
  });

  it('disables action buttons when the spec is already approved', async () => {
    stubFetch([
      (url) =>
        url.endsWith('/candidates/biz-1')
          ? json(200, fakeCandidate({ spec: fakeSpec({ status: 'approved' }) }))
          : null,
    ]);
    renderCandidate();
    await waitFor(() => screen.getByRole('button', { name: 'Approve' }));
    expect((screen.getByRole('button', { name: 'Approve' }) as HTMLButtonElement).disabled).toBe(
      true
    );
    expect((screen.getByRole('button', { name: 'Reject' }) as HTMLButtonElement).disabled).toBe(
      true
    );
  });

  it('surfaces server errors from Approve in the action-error pane', async () => {
    stubFetch([
      (url) => (url.endsWith('/candidates/biz-1') ? json(200, fakeCandidate()) : null),
      (url, init) =>
        url.endsWith('/specs/spec-1/approve') && init?.method === 'POST'
          ? new Response('boom', { status: 502 })
          : null,
    ]);
    renderCandidate();
    await waitFor(() => screen.getByRole('button', { name: 'Approve' }));
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => {
      expect(screen.getByTestId('action-error')).toHaveTextContent(/HTTP 502/);
    });
  });
});
