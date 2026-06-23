// file: web/tests/e2e/auth-flow.spec.ts
// version: 1.1.0
// guid: 9b9cd01d-ea34-4d87-bc84-f390b6ef10cd
// last-edited: 2026-06-23

import { test, expect } from '@playwright/test';
import { setupPhase2Interactive } from './utils/test-helpers';

test.describe('Authentication Flow', () => {
  test('redirects protected routes to login when auth is required', async ({
    page,
  }) => {
    await setupPhase2Interactive(page, undefined, {
      auth: {
        has_users: true,
        requires_auth: true,
        bootstrap_ready: false,
        login_username: 'admin',
        login_password: 'secretpass123',
      },
    });

    await page.goto('/dashboard');
    await expect(page).toHaveURL(/\/login$/);
    await expect(
      page.getByRole('heading', { name: 'Login' })
    ).toBeVisible();
  });

  test('supports first-run admin setup and login', async ({ page }) => {
    await setupPhase2Interactive(page, undefined, {
      auth: {
        has_users: false,
        requires_auth: true,
        bootstrap_ready: true,
      },
    });

    await page.goto('/login');
    await expect(
      page.getByRole('heading', { name: 'Create Admin Account' })
    ).toBeVisible();

    await page.getByLabel('Username').fill('first-admin');
    await page.getByLabel('Email (optional)').fill('admin@example.com');
    await page.getByLabel('Password').fill('very-strong-password');
    await page.getByRole('button', { name: 'Create And Login' }).click();

    await expect(page).toHaveURL(/\/dashboard$/);
    await expect(page.getByLabel('logout')).toBeVisible();
  });

  test('shows invalid-credential error before successful login', async ({
    page,
  }) => {
    await setupPhase2Interactive(page, undefined, {
      auth: {
        has_users: true,
        requires_auth: true,
        bootstrap_ready: false,
        login_username: 'admin',
        login_password: 'secretpass123',
      },
    });

    await page.goto('/login');
    await page.getByLabel('Username').fill('admin');
    await page.getByLabel('Password').fill('wrong-password');
    await page.getByRole('button', { name: 'Login' }).click();
    await expect(page.getByText('invalid credentials')).toBeVisible();

    await page.getByLabel('Password').fill('secretpass123');
    await page.getByRole('button', { name: 'Login' }).click();
    await expect(page).toHaveURL(/\/dashboard$/);
  });
});

// Real-server smoke test — exercises actual cookie/session flow against the
// embedded Go server started by playwright.config.ts webServer. Skips if the
// DB already has users (local reuse) so it doesn't conflict with CI fresh-DB
// semantics.
test.describe('Authentication — Real Server Smoke', () => {
  test('first-run bootstrap creates admin account and sets real session cookie', async ({
    page,
    request,
  }) => {
    // Check live server auth status — no mocks used in this test.
    const status = await request.get('/api/v1/auth/status');
    if (!status.ok()) {
      test.skip(true, 'Auth status endpoint unavailable — skipping real-server smoke test');
      return;
    }
    const body = await status.json() as { requires_auth?: boolean; has_users?: boolean };
    if (!body.requires_auth || body.has_users) {
      test.skip(
        true,
        `Server already bootstrapped (requires_auth=${body.requires_auth}, has_users=${body.has_users}) — skip to avoid state pollution`
      );
      return;
    }

    // Fresh DB: exercise the real first-run path.
    await page.goto('/login');
    await expect(
      page.getByRole('heading', { name: 'Create Admin Account' })
    ).toBeVisible({ timeout: 10000 });

    await page.getByLabel('Username').fill('e2e-smoke-admin');
    await page.getByLabel('Password').fill('e2e-smoke-pass-9');
    await page.getByRole('button', { name: 'Create And Login' }).click();

    // Real session cookie must be set — the redirect only works with a valid cookie.
    await expect(page).toHaveURL(/\/dashboard$/, { timeout: 10000 });
    await expect(page.getByLabel('logout')).toBeVisible();

    // Confirm the session survives a reload (cookie persists across navigation).
    await page.reload();
    await expect(page).toHaveURL(/\/dashboard$/);
  });
});
