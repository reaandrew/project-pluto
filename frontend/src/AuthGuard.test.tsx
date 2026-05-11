import type { ComponentType } from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';

// AuthGuard pulls COGNITO_LOGIN_URL from api.ts, which is computed at
// module-load from window.__FINANCE_CONFIG__. Setting the global before
// vi.resetModules() and re-importing the SUT makes the constant pick up
// the test config for each case.
async function freshGuard(cfg: Record<string, unknown>): Promise<ComponentType> {
  (window as unknown as { __FINANCE_CONFIG__: object }).__FINANCE_CONFIG__ = cfg;
  vi.resetModules();
  const mod = await import('./AuthGuard');
  return mod.default;
}

beforeEach(() => {
  // Clear any cookie set by a prior test.
  document.cookie = 'auth_token=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/';
});

afterEach(() => {
  vi.restoreAllMocks();
});

function renderWithGuard(Guard: ComponentType): void {
  render(
    <MemoryRouter initialEntries={['/']}>
      <Routes>
        <Route element={<Guard />}>
          <Route index element={<p>protected content</p>} />
        </Route>
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

  it('redirects to Cognito when cookie is absent', async () => {
    const replace = vi.fn();
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...window.location, replace, assign: vi.fn() },
    });

    const Guard = await freshGuard({
      bffBaseUrl: '',
      apiBaseUrl: '',
      environment: 'unit-test',
      gitSha: 'x',
      cognitoHostedLoginUrl: 'https://example.com/login',
    });
    renderWithGuard(Guard);

    expect(replace).toHaveBeenCalledWith('https://example.com/login');
    // Protected content must NOT be rendered while redirecting.
    expect(screen.queryByText('protected content')).not.toBeInTheDocument();
    // The placeholder explains the redirect.
    expect(screen.getByText(/Redirecting to sign-in/i)).toBeInTheDocument();
  });

  it('does NOT redirect when cognitoHostedLoginUrl is empty (local dev)', async () => {
    const replace = vi.fn();
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...window.location, replace, assign: vi.fn() },
    });

    const Guard = await freshGuard({
      bffBaseUrl: '',
      apiBaseUrl: '',
      environment: 'local',
      gitSha: 'dev',
      cognitoHostedLoginUrl: '',
    });
    renderWithGuard(Guard);

    expect(replace).not.toHaveBeenCalled();
    expect(screen.queryByText('protected content')).not.toBeInTheDocument();
    expect(screen.getByText(/Not signed in/i)).toBeInTheDocument();
  });

  it('matches auth_token exactly, not a prefix', async () => {
    // A different cookie whose name starts with auth_token shouldn't count.
    document.cookie = 'auth_token_other=anything; path=/';
    const replace = vi.fn();
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...window.location, replace, assign: vi.fn() },
    });
    const Guard = await freshGuard({
      bffBaseUrl: '',
      apiBaseUrl: '',
      environment: 'unit-test',
      gitSha: 'x',
      cognitoHostedLoginUrl: 'https://example.com/login',
    });
    renderWithGuard(Guard);
    expect(replace).toHaveBeenCalledWith('https://example.com/login');
  });
});
