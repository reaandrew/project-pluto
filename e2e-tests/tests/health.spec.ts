import { test, expect, request } from '@playwright/test';

const API_URL = process.env.API_URL ?? 'http://localhost:8080';
const BFF_URL = process.env.BFF_URL ?? 'http://localhost:8080';
const BASE_URL = process.env.BASE_URL ?? 'http://localhost:5173';
const ENVIRONMENT = process.env.ENVIRONMENT ?? 'unknown';

test.describe('health', () => {
  test('API /health returns expected env', async () => {
    const ctx = await request.newContext();
    const res = await ctx.get(`${API_URL}/health`);
    expect(res.status()).toBe(200);
    const body = await res.json();
    expect(body.message).toBe('hello from ai-website-agency');
    if (ENVIRONMENT !== 'unknown') {
      expect(body.env).toBe(ENVIRONMENT);
    }
    expect(body.ts).toBeGreaterThan(0);
  });

  test('BFF /health proxies API and returns expected env', async () => {
    const ctx = await request.newContext();
    const res = await ctx.get(`${BFF_URL}/health`);
    expect(res.status()).toBe(200);
    const body = await res.json();
    expect(body.message).toBe('hello from ai-website-agency');
    if (ENVIRONMENT !== 'unknown') {
      expect(body.env).toBe(ENVIRONMENT);
    }
  });

  test('frontend loads and shows env badge', async ({ page }) => {
    await page.goto(BASE_URL);
    await expect(page.locator('h1')).toHaveText('ai-website-agency');
    if (ENVIRONMENT !== 'unknown') {
      await expect(page.locator('code').first()).toHaveText(ENVIRONMENT);
    }
    // Dashboard h2 renders immediately. The h3 only appears once the BFF
    // /health fetch resolves and Dashboard.tsx swaps out the "Loading…"
    // state — that's the signal the round-trip worked.
    await expect(page.locator('h2')).toHaveText('Dashboard');
    await expect(page.locator('h3')).toContainText('BFF /health', { timeout: 15_000 });
  });
});
