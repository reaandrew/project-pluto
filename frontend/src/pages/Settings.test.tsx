import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Settings, { computeDailyCostUsd } from './Settings';
import type { PipelineSettings } from '../api';

// Settings uses <Link to="/settings/targeting"> for the sub-page
// pointer — react-router-dom requires a Router context. Wrapping
// here keeps the tests independent of main.tsx's full route tree.
function renderSettings() {
  return render(
    <MemoryRouter>
      <Settings />
    </MemoryRouter>
  );
}

const defaults: PipelineSettings = {
  pipelineEnabled: true,
  stages: {
    discoveryEnabled: true,
    auditEnabled: true,
    previewEnabled: true,
    outreachEnabled: false,
  },
  caps: {
    maxDiscoveriesPerDay: 100,
    maxAuditsPerDay: 50,
    maxPreviewsPerDay: 10,
    maxEmailsPerDay: 5,
    maxReviewQueueSize: 20,
    maxBacklogSize: 500,
  },
  thresholds: {
    minTechnicalIssueScore: 30,
    minQualificationScore: 70,
    minContactConfidence: 0.6,
  },
  budgets: {
    dailyBedrockUsd: 5,
    dailyPlacesUsd: 2,
    dailyEmailUsd: 1,
  },
};

beforeEach(() => {
  (window as unknown as { __FINANCE_CONFIG__: object }).__FINANCE_CONFIG__ = {
    bffBaseUrl: 'https://test-bff.example.com',
    apiBaseUrl: 'https://test-api.example.com',
    environment: 'unit-test',
    gitSha: 'x',
  };
});

function stubFetch(map: Record<string, (init?: RequestInit) => Response>) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const handler = Object.entries(map).find(([k]) => url.endsWith(k))?.[1];
      if (!handler) throw new Error(`Unstubbed fetch: ${url}`);
      return Promise.resolve(handler(init));
    })
  );
}

describe('Settings', () => {
  it('renders loading then the master toggle and caps', async () => {
    stubFetch({
      '/settings': () => json(200, defaults),
    });
    renderSettings();
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByLabelText('pipelineEnabled')).toBeInTheDocument();
    });
    const masterToggle = screen.getByLabelText('pipelineEnabled') as HTMLInputElement;
    expect(masterToggle.checked).toBe(true);

    // Cap inputs render with seed values.
    const audits = screen.getByLabelText('Max audits / day') as HTMLInputElement;
    expect(audits.value).toBe('50');
  });

  it('renders an error message when GET fails', async () => {
    stubFetch({
      '/settings': () => new Response('boom', { status: 500 }),
    });
    renderSettings();
    await waitFor(() => {
      expect(screen.getByText(/Could not load settings/i)).toBeInTheDocument();
    });
  });

  it('flagged budget-paused stages show the auto-paused tag', async () => {
    const paused: PipelineSettings = {
      ...defaults,
      stages: { ...defaults.stages, auditEnabled: false },
      stagePauseReasons: { audit: 'budget' },
    };
    stubFetch({ '/settings': () => json(200, paused) });
    renderSettings();
    await waitFor(() => {
      expect(screen.getByText(/auto-paused: budget/i)).toBeInTheDocument();
    });
  });

  it('PATCH submission flips the master switch and shows Saved', async () => {
    const captured: RequestInit[] = [];
    stubFetch({
      '/settings': (init) => {
        if (init?.method === 'PATCH') {
          captured.push(init);
          // Echo back the submitted body so the page reflects it.
          const body = JSON.parse(String(init.body));
          return json(200, body);
        }
        return json(200, defaults);
      },
    });
    renderSettings();
    await waitFor(() => screen.getByLabelText('pipelineEnabled'));

    fireEvent.click(screen.getByLabelText('pipelineEnabled'));
    fireEvent.click(screen.getByRole('button', { name: /Save changes/i }));

    await waitFor(() => {
      expect(screen.getByText('Saved.')).toBeInTheDocument();
    });

    expect(captured).toHaveLength(1);
    const sent = JSON.parse(String(captured[0].body));
    expect(sent.pipelineEnabled).toBe(false);
    // Whole-object PATCH: caps, stages etc. all come along.
    expect(sent.caps.maxAuditsPerDay).toBe(50);
  });

  it('shows the cost preview total', async () => {
    stubFetch({ '/settings': () => json(200, defaults) });
    renderSettings();
    await waitFor(() => screen.getByLabelText('pipelineEnabled'));
    // Defaults give a specific computed total; assert the section is there
    // and the total figure appears.
    const previewSection = screen.getByLabelText('cost-preview');
    expect(previewSection).toBeInTheDocument();
    const expected = computeDailyCostUsd(defaults).totalUsd;
    expect(previewSection.textContent).toContain(`Total daily: $${expected.toFixed(2)}`);
  });

  it('shows a save-failed message on PATCH error', async () => {
    stubFetch({
      '/settings': (init) =>
        init?.method === 'PATCH' ? new Response('nope', { status: 403 }) : json(200, defaults),
    });
    renderSettings();
    await waitFor(() => screen.getByLabelText('pipelineEnabled'));
    fireEvent.click(screen.getByRole('button', { name: /Save changes/i }));
    await waitFor(() => {
      expect(screen.getByText(/Save failed/i)).toBeInTheDocument();
    });
  });
});

describe('computeDailyCostUsd', () => {
  it('returns documented spec totals for the default caps', () => {
    const r = computeDailyCostUsd(defaults);
    expect(r.discoveryUsd).toBeCloseTo(0.51); // 100 * 0.3 * 0.017
    expect(r.auditUsd).toBeCloseTo(0.6); // 50 * 0.012
    expect(r.previewUsd).toBeCloseTo(0.755); // 10 * (0.075 + 0.0005)
    expect(r.outreachUsd).toBeCloseTo(0.0255); // 5 * (0.005 + 0.0001)
    expect(r.totalUsd).toBeCloseTo(0.51 + 0.6 + 0.755 + 0.0255);
  });

  it('scales linearly with caps', () => {
    const r1 = computeDailyCostUsd(defaults);
    const r2 = computeDailyCostUsd({
      ...defaults,
      caps: { ...defaults.caps, maxAuditsPerDay: 100 },
    });
    expect(r2.auditUsd).toBeCloseTo(r1.auditUsd * 2);
  });
});

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}
