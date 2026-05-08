import { describe, it, expect, vi, beforeEach } from 'vitest';

// Set runtime config before importing the module.
beforeEach(() => {
  (window as unknown as { __FINANCE_CONFIG__: object }).__FINANCE_CONFIG__ = {
    bffBaseUrl: 'https://test-bff.example.com',
    apiBaseUrl: 'https://test-api.example.com',
    environment: 'unit-test',
    gitSha: 'abc1234',
  };
  vi.resetModules();
});

describe('api', () => {
  it('getHealth returns parsed JSON on 200', async () => {
    const fakeResp: object = {
      message: 'hi',
      env: 'unit-test',
      ts: 1,
      items_table: 'website-agency-items-unit-test',
    };
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify(fakeResp), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        )
      )
    );
    const { getHealth } = await import('./api');
    const res = await getHealth();
    expect(res.env).toBe('unit-test');
  });

  it('getHealth throws on non-2xx', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(new Response('boom', { status: 500 })))
    );
    const { getHealth } = await import('./api');
    await expect(getHealth()).rejects.toThrow(/HTTP 500/);
  });
});
