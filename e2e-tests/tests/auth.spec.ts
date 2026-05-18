import { test, expect, type Page, type Response } from '@playwright/test';

// Real headless operator login through the Cognito Hosted UI — the gate
// that would have caught BOTH production regressions we shipped blind:
//
//   1. Blank screen  — relative `vite base` made assets 404 off the SPA
//      sub-path, so the app rendered nothing.
//   2. 401 everywhere — the cookie→Authorization CloudFront Function
//      errored on the 2.0 runtime and the auth_token cookie was
//      host-only, so every authenticated BFF call returned 401.
//
// This spec drives the actual browser flow (SPA → Cognito Hosted UI →
// PKCE callback → cookie → BFF) against the deployed per-PR env and
// asserts the operator screens render with live data and that NO
// authenticated BFF call comes back 401/403. It fails the PR if the
// product is not actually usable — regardless of unit tests passing.

const BASE_URL = process.env.BASE_URL ?? '';
const BFF_URL = process.env.BFF_URL ?? '';
const USER = process.env.E2E_TEST_USER ?? '';
const PASS = process.env.E2E_TEST_PASS ?? '';
const ENVIRONMENT = process.env.ENVIRONMENT ?? '';
const IS_CI = !!process.env.CI;

// This gate runs on per-PR preview envs only. Protected/shared envs
// have no throwaway seeded operator (seed-test-user.sh refuses them —
// skeleton pitfall #13), so skip there rather than fail; production
// auth is verified out-of-band.
const PROTECTED = /^(production|main|master|prod|develop)$/.test(ENVIRONMENT);

// Locally (no creds) skip; in CI on a preview env missing creds is a
// hard failure so the gate can't silently no-op.
const haveCreds = !!(BASE_URL && BFF_URL && USER && PASS);
if (PROTECTED) {
  test.skip(true, `protected env '${ENVIRONMENT}' — auth e2e is preview-only (pitfall #13)`);
} else if (!haveCreds && !IS_CI) {
  test.skip(true, 'auth e2e needs BASE_URL/BFF_URL/E2E_TEST_USER/E2E_TEST_PASS — skipped locally');
}

test.describe('operator auth + screens (real Hosted UI login)', () => {
  test.describe.configure({ mode: 'serial' });

  test('CI has the required env wired', () => {
    expect(
      haveCreds,
      'BASE_URL, BFF_URL, E2E_TEST_USER, E2E_TEST_PASS must all be set in CI ' +
        '(repo/org secrets E2E_TEST_USER / E2E_TEST_PASS)'
    ).toBeTruthy();
  });

  test('logs in, screens render, zero 401/403 on the BFF', async ({ page }) => {
    test.skip(!haveCreds, 'env not wired');
    test.setTimeout(120_000);

    // Record every BFF response so a single 401/403 fails the gate even
    // if the UI degrades gracefully and hides it.
    const authFailures: string[] = [];
    page.on('response', (res: Response) => {
      const url = res.url();
      if (url.startsWith(BFF_URL) && (res.status() === 401 || res.status() === 403)) {
        authFailures.push(`${res.status()} ${res.request().method()} ${url}`);
      }
    });

    // 1. Hit the app cold → AuthGuard bounces to the Cognito Hosted UI.
    await page.goto(BASE_URL, { waitUntil: 'domcontentloaded' });
    await page.waitForLoadState('networkidle').catch(() => {});

    if (/amazoncognito\.com/.test(page.url())) {
      await loginViaHostedUI(page);
    }

    // 2. Back in the SPA: not blank, shell rendered.
    await page.waitForURL((u) => !/amazoncognito\.com/.test(u.href), { timeout: 60_000 });
    await expect(page.locator('h1')).toHaveText('ai-website-agency', { timeout: 30_000 });
    await expect(page.locator('#root')).not.toBeEmpty();

    // 3. Dashboard proves the cookie→CF-function→BFF round-trip works:
    //    the h3 only swaps in once GET {BFF}/health resolves.
    await expect(page.locator('h2')).toHaveText('Dashboard');
    await expect(page.locator('h3')).toContainText('BFF /health', { timeout: 20_000 });

    // 4. Operator-only screens: these widgets only render when their
    //    authenticated BFF calls return 200. Under the prod bug they
    //    were 401 and these never appeared.
    await page.getByRole('link', { name: 'Queue' }).click();
    await expect(page.getByRole('heading', { name: 'Review queue' })).toBeVisible({
      timeout: 20_000,
    });

    await page.getByRole('link', { name: 'Metrics' }).click();
    await expect(page.getByRole('heading', { name: 'Metrics', level: 2 })).toBeVisible();
    await expect(
      page.getByRole('heading', { name: /Discoveries — last 7 days/ })
    ).toBeVisible({ timeout: 20_000 });

    await page.getByRole('link', { name: 'Settings' }).click();
    await expect(page.getByRole('heading', { name: /Settings/i })).toBeVisible({
      timeout: 20_000,
    });

    // 5. Hard auth assertion — the defect this gate exists for.
    expect(
      authFailures,
      `Authenticated BFF calls returned 401/403:\n${authFailures.join('\n')}`
    ).toEqual([]);
  });
});

// loginViaHostedUI fills the Cognito managed login form. Selectors are
// layered because the managed UI markup varies (classic vs. 2024+
// managed login); we match by type/name/role rather than brittle ids.
async function loginViaHostedUI(page: Page): Promise<void> {
  const email = page
    .locator('input[type="email"], input[name="username"], input[type="text"]')
    .first();
  await email.waitFor({ state: 'visible', timeout: 30_000 });
  await email.fill(USER);

  const pw = page.locator('input[type="password"], input[name="password"]').first();
  await pw.fill(PASS);

  const submit = page
    .locator(
      'button[type="submit"], input[type="submit"], button:has-text("Sign in"), [name="signInSubmitButton"]'
    )
    .first();
  await Promise.all([
    page.waitForURL((u) => !/\/login(\?|$)/.test(u.href), { timeout: 60_000 }).catch(() => {}),
    submit.click(),
  ]);
}
