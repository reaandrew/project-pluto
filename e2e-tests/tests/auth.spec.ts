import { test } from '@playwright/test';

// Placeholder — wire up once auth Lambdas land.
// Skeleton intentionally has no auth so the deploy pipeline is exercised end-to-end
// without security flows blocking.

test.describe('auth (placeholder)', () => {
  test.skip('login/logout flow', async () => {
    // TODO: implement when /auth/login + /auth/me exist
  });
});
