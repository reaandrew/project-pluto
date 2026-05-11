import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import App from './App';
import Dashboard from './pages/Dashboard';
import Login from './pages/Login';
import Queue from './pages/Queue';
import Settings from './pages/Settings';

beforeEach(() => {
  (window as unknown as { __FINANCE_CONFIG__: object }).__FINANCE_CONFIG__ = {
    bffBaseUrl: 'https://test-bff.example.com',
    apiBaseUrl: 'https://test-api.example.com',
    environment: 'unit-test',
    gitSha: 'abc1234',
  };
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            message: 'hi',
            env: 'unit-test',
            ts: 1,
            items_table: 'items-test',
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
      )
    )
  );
});

// renderAt wires the same routing tree main.tsx uses, scoped to a starting
// path so each test exercises one route in isolation. Keeps the test file
// independent of main.tsx (which mounts to a real DOM root).
function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/" element={<App />}>
          <Route index element={<Dashboard />} />
          <Route path="queue" element={<Queue />} />
          <Route path="settings" element={<Settings />} />
          <Route path="login" element={<Login />} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}

describe('admin shell routing', () => {
  it('renders the shell header on every route', () => {
    renderAt('/queue');
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('ai-website-agency');
  });

  it('renders Dashboard at "/"', async () => {
    renderAt('/');
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: 'Dashboard' })).toBeInTheDocument();
    });
    expect(screen.getByRole('heading', { name: 'BFF /health' })).toBeInTheDocument();
  });

  it('renders Queue at "/queue"', () => {
    renderAt('/queue');
    expect(screen.getByRole('heading', { name: 'Review queue' })).toBeInTheDocument();
  });

  it('renders Settings at "/settings"', () => {
    renderAt('/settings');
    expect(screen.getByRole('heading', { name: 'Pipeline settings' })).toBeInTheDocument();
  });

  it('renders Login at "/login"', () => {
    renderAt('/login');
    expect(screen.getByRole('heading', { name: 'Sign in' })).toBeInTheDocument();
  });

  it('marks the active nav link as active', () => {
    renderAt('/queue');
    const link = screen.getByRole('link', { name: 'Queue' });
    // react-router-dom's NavLink adds aria-current="page" to the active link.
    expect(link).toHaveAttribute('aria-current', 'page');
  });
});
