import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import Targeting from './Targeting';
import type { TargetingProfile } from '../api';

const sample: TargetingProfile = {
  id: 'id-1',
  vertical: 'accountants',
  location: 'Manchester, UK',
  includeKeywords: ['chartered', 'tax'],
  excludeKeywords: ['franchise'],
  weights: {
    websiteAge: 0.2,
    auditScore: 0.3,
    businessSize: 0.2,
    contactConfidence: 0.2,
    verticalFit: 0.1,
  },
  enabled: true,
  stats: { discovered7d: 12, qualified7d: 4, approved7d: 1 },
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-05-01T00:00:00Z',
  etag: 'etag-1',
};

beforeEach(() => {
  (window as unknown as { __FINANCE_CONFIG__: object }).__FINANCE_CONFIG__ = {
    bffBaseUrl: 'https://test-bff.example.com',
    apiBaseUrl: 'https://test-api.example.com',
    environment: 'unit-test',
    gitSha: 'x',
  };
});

// stubFetch lets a test wire arbitrary responses to specific URLs +
// methods. The matcher checks whether the request URL ends with the
// key, so callers can use short suffixes like "/targeting" or
// "/targeting/id-1".
function stubFetch(map: Record<string, (init?: RequestInit) => Response>) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      // Method-aware lookup: caller can prefix with METHOD: to disambiguate.
      const method = (init?.method ?? 'GET').toUpperCase();
      const keys = Object.keys(map);
      // Try exact method-prefixed match first, then plain suffix.
      const methodPrefix = method + ' ';
      let handler =
        map[
          keys.find(
            (k) => k.startsWith(methodPrefix) && url.endsWith(k.slice(method.length + 1))
          ) ?? ''
        ];
      if (!handler) {
        handler = map[keys.find((k) => url.endsWith(k)) ?? ''];
      }
      if (!handler) throw new Error(`Unstubbed fetch: ${method} ${url}`);
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

describe('Targeting', () => {
  it('renders loading then a list of profiles', async () => {
    stubFetch({
      '/targeting': () => json(200, { profiles: [sample] }),
    });
    render(<Targeting />);
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText('accountants')).toBeInTheDocument();
    });
    expect(screen.getByText('Manchester, UK')).toBeInTheDocument();
  });

  it('renders an empty-state hint when no profiles', async () => {
    stubFetch({
      '/targeting': () => json(200, { profiles: [] }),
    });
    render(<Targeting />);
    await waitFor(() => {
      expect(screen.getByText(/No profiles yet/i)).toBeInTheDocument();
    });
  });

  it('clicking a profile opens the editor pre-filled', async () => {
    stubFetch({
      '/targeting': () => json(200, { profiles: [sample] }),
    });
    render(<Targeting />);
    await waitFor(() => screen.getByText('accountants'));
    fireEvent.click(screen.getByRole('button', { name: 'accountants' }));
    expect(screen.getByRole('heading', { name: /Edit: accountants/i })).toBeInTheDocument();
    const vertical = screen.getByLabelText('Vertical') as HTMLInputElement;
    expect(vertical.value).toBe('accountants');
  });

  it('"New profile" opens the editor with sensible defaults', async () => {
    stubFetch({
      '/targeting': () => json(200, { profiles: [] }),
    });
    render(<Targeting />);
    await waitFor(() => screen.getByRole('button', { name: /New profile/i }));
    fireEvent.click(screen.getByRole('button', { name: /New profile/i }));
    expect(screen.getByRole('heading', { name: /New targeting profile/i })).toBeInTheDocument();
    // Sum-of-weights placeholder reads 1.00 ✓.
    expect(screen.getByLabelText('weight-sum').textContent).toMatch(/1\.00\s*✓/);
  });

  it('blocks Save when weights do not sum to 1.0', async () => {
    stubFetch({
      '/targeting': () => json(200, { profiles: [sample] }),
    });
    render(<Targeting />);
    await waitFor(() => screen.getByText('accountants'));
    fireEvent.click(screen.getByRole('button', { name: 'accountants' }));

    const websiteAge = screen.getByLabelText('Website age') as HTMLInputElement;
    fireEvent.change(websiteAge, { target: { value: '0.5' } });

    const sum = screen.getByLabelText('weight-sum');
    expect(sum.textContent).toMatch(/must be 1\.0/);
    const save = screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
  });

  it('POSTs a new profile and refreshes the list', async () => {
    const captured: { url: string; init: RequestInit | undefined }[] = [];
    let listCallCount = 0;
    stubFetch({
      '/targeting': (init) => {
        const method = (init?.method ?? 'GET').toUpperCase();
        if (method === 'POST') {
          captured.push({ url: '/targeting', init });
          return json(201, { ...sample, id: 'id-new' });
        }
        listCallCount++;
        return json(200, {
          profiles: listCallCount === 1 ? [] : [{ ...sample, id: 'id-new' }],
        });
      },
    });
    render(<Targeting />);
    await waitFor(() => screen.getByRole('button', { name: /New profile/i }));
    fireEvent.click(screen.getByRole('button', { name: /New profile/i }));

    fireEvent.change(screen.getByLabelText('Vertical'), { target: { value: 'plumbers' } });
    fireEvent.change(screen.getByLabelText('Location'), { target: { value: 'Leeds' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => screen.getByText('accountants'));

    expect(captured.length).toBe(1);
    const sent = JSON.parse(String(captured[0].init?.body));
    expect(sent.vertical).toBe('plumbers');
    expect(sent.location).toBe('Leeds');
  });

  it('PATCH submission sends If-Match header carrying the current etag', async () => {
    let patchInit: RequestInit | undefined;
    stubFetch({
      '/targeting': () => json(200, { profiles: [sample] }),
      '/targeting/id-1': (init) => {
        patchInit = init;
        return json(200, sample);
      },
    });
    render(<Targeting />);
    await waitFor(() => screen.getByText('accountants'));
    fireEvent.click(screen.getByRole('button', { name: 'accountants' }));
    fireEvent.change(screen.getByLabelText('Vertical'), { target: { value: 'lawyers' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => {
      expect(patchInit).toBeDefined();
    });
    expect(patchInit?.method).toBe('PATCH');
    const headers = patchInit?.headers as Record<string, string>;
    expect(headers['If-Match']).toBe('etag-1');
    const body = JSON.parse(String(patchInit?.body));
    expect(body.vertical).toBe('lawyers');
  });

  it('shows save-failed when PATCH returns 412 (etag mismatch)', async () => {
    stubFetch({
      '/targeting': () => json(200, { profiles: [sample] }),
      '/targeting/id-1': () => new Response('stale', { status: 412 }),
    });
    render(<Targeting />);
    await waitFor(() => screen.getByText('accountants'));
    fireEvent.click(screen.getByRole('button', { name: 'accountants' }));
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(screen.getByText(/Save failed/)).toBeInTheDocument();
    });
  });
});
