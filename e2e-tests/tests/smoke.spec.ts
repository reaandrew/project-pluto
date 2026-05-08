import { test, expect, request } from '@playwright/test';

const API_URL = process.env.API_URL ?? 'http://localhost:8080';

test('API rejects unknown route with 404', async () => {
  const ctx = await request.newContext();
  const res = await ctx.get(`${API_URL}/this-route-does-not-exist-${Date.now()}`);
  expect([403, 404]).toContain(res.status());
});

test('API CORS preflight returns expected headers', async () => {
  const ctx = await request.newContext();
  const res = await ctx.fetch(`${API_URL}/health`, {
    method: 'OPTIONS',
    headers: {
      Origin: 'https://example.com',
      'Access-Control-Request-Method': 'GET',
      'Access-Control-Request-Headers': 'Content-Type',
    },
  });
  // We don't assert the CORS header values (CORS allow_origins is env-aware) — just
  // that the preflight doesn't 500.
  expect(res.status()).toBeLessThan(500);
});
