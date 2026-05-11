import type { ComponentType } from 'react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';

async function freshGuard(cfg: Record<string, unknown>): Promise<ComponentType> {
  (window as unknown as { __FINANCE_CONFIG__: object }).__FINANCE_CONFIG__ = cfg;
  vi.resetModules();
  const mod = await import('./AuthGuard');
  return mod.default;
}

beforeEach(() => {
  document.cookie = 'auth_token=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/';
});

function renderWithGuard(Guard: ComponentType): void {
  render(
    <MemoryRouter initialEntries={['/']}>
      <Routes>
        <Route element={<Guard />}>
          <Route index element={<p>protected content</p>} />
        </Route>
        <Route path="/login" element={<p>login page</p>} />
      </Routes>
    </MemoryRouter>
  );
}

describe('AuthGuard', () => {
  it('renders the protected outlet when auth_token cookie is present', async () => {
    document.cookie = 'auth_token=abc123; path=/';
    const Guard = await freshGuard({
      bffBaseUrl: '',
      apiBaseUrl: '',
      environment: 'unit-test',
      gitSha: 'x',
      cognitoHostedLoginUrl: 'https://example.com/login',
    });
    renderWithGuard(Guard);
    expect(screen.getByText('protected content')).toBeInTheDocument();
  });

  it('navigates to /login when cookie is absent and Cognito is configured', async () => {
    const Guard = await freshGuard({
      bffBaseUrl: '',
      apiBaseUrl: '',
      environment: 'unit-test',
      gitSha: 'x',
      cognitoHostedLoginUrl: 'https://example.com/login',
    });
    renderWithGuard(Guard);
    // <Navigate to="/login" replace /> renders the matching public
    // route — verify that the login page renders, not the protected one.
    expect(screen.getByText('login page')).toBeInTheDocument();
    expect(screen.queryByText('protected content')).not.toBeInTheDocument();
  });

  it('shows the "Not signed in" placeholder when cognitoHostedLoginUrl is empty (local dev)', async () => {
    const Guard = await freshGuard({
      bffBaseUrl: '',
      apiBaseUrl: '',
      environment: 'local',
      gitSha: 'dev',
      cognitoHostedLoginUrl: '',
    });
    renderWithGuard(Guard);
    expect(screen.queryByText('protected content')).not.toBeInTheDocument();
    expect(screen.getByText(/Not signed in/i)).toBeInTheDocument();
  });

  it('matches auth_token exactly, not a prefix', async () => {
    document.cookie = 'auth_token_other=anything; path=/';
    const Guard = await freshGuard({
      bffBaseUrl: '',
      apiBaseUrl: '',
      environment: 'unit-test',
      gitSha: 'x',
      cognitoHostedLoginUrl: 'https://example.com/login',
    });
    renderWithGuard(Guard);
    expect(screen.getByText('login page')).toBeInTheDocument();
  });
});
