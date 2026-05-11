import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import Callback from './Callback';

beforeEach(() => {
  (window as unknown as { __FINANCE_CONFIG__: object }).__FINANCE_CONFIG__ = {
    bffBaseUrl: '',
    apiBaseUrl: '',
    environment: 'unit-test',
    gitSha: 'x',
    cognitoHostedLoginUrl: 'https://login.example.com/login',
    cognitoAuthOrigin: 'auth.example.com',
    cognitoClientId: 'test-client',
    cognitoRedirectUri: 'https://app.example.com/oauth/callback',
  };
  // Clean up auth_token + verifier before each test.
  document.cookie = 'auth_token=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/';
  sessionStorage.clear();
});

function renderCallback(query: string) {
  return render(
    <MemoryRouter initialEntries={[`/oauth/callback?${query}`]}>
      <Routes>
        <Route path="/oauth/callback" element={<Callback />} />
        <Route path="/" element={<p>home page</p>} />
      </Routes>
    </MemoryRouter>
  );
}

describe('Callback', () => {
  it('shows an error when no code query param is present', () => {
    renderCallback('');
    expect(screen.getByText('Sign-in failed')).toBeInTheDocument();
    expect(screen.getByText(/No `code` query parameter/)).toBeInTheDocument();
  });

  it('shows the Cognito-side error when ?error= is present', () => {
    renderCallback('error=access_denied&error_description=user+cancelled');
    expect(screen.getByText('Sign-in failed')).toBeInTheDocument();
    expect(screen.getByText(/access_denied/)).toBeInTheDocument();
  });

  it('shows an error when the PKCE verifier is missing from sessionStorage', async () => {
    // sessionStorage is empty (verifier not set).
    renderCallback('code=abc');
    await waitFor(() => {
      expect(screen.getByText(/Missing PKCE verifier/i)).toBeInTheDocument();
    });
  });

  it('exchanges code, sets cookie, and navigates home on success', async () => {
    sessionStorage.setItem('pkce_verifier', 'v123');
    const fetchSpy = vi.fn((_url: string, _init?: RequestInit) =>
      Promise.resolve(
        new Response(JSON.stringify({ id_token: 'eyJ-id-token' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      )
    );
    vi.stubGlobal('fetch', fetchSpy);

    renderCallback('code=abc');

    await waitFor(() => {
      expect(screen.getByText('home page')).toBeInTheDocument();
    });

    // POST to Cognito /oauth2/token.
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url, init] = fetchSpy.mock.calls[0];
    expect(url).toBe('https://auth.example.com/oauth2/token');
    expect(init?.method).toBe('POST');
    const body = (init?.body as string) ?? '';
    expect(body).toContain('grant_type=authorization_code');
    expect(body).toContain('code=abc');
    expect(body).toContain('code_verifier=v123');

    // auth_token cookie set with the id_token value.
    expect(document.cookie).toContain('auth_token=eyJ-id-token');
    // verifier wiped so a follow-up callback hit can't replay.
    expect(sessionStorage.getItem('pkce_verifier')).toBeNull();
  });

  it('surfaces the Cognito error body on token-exchange failure', async () => {
    sessionStorage.setItem('pkce_verifier', 'v123');
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(new Response('invalid_grant', { status: 400 })))
    );

    renderCallback('code=abc');

    await waitFor(() => {
      expect(screen.getByText(/Token exchange failed.*400.*invalid_grant/)).toBeInTheDocument();
    });
  });
});
