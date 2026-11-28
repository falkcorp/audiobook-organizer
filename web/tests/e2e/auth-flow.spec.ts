// file: web/tests/e2e/auth-flow.spec.ts
// version: 1.2.0
// guid: 9b9cd01d-ea34-4d87-bc84-f390b6ef10cd
// last-edited: 2026-08-09

import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect, type Page } from '@playwright/test';
import { setupPhase2Interactive } from './utils/test-helpers';
import { paths, urlEndsWith } from './utils/paths';
import { E2E_TMP_PREFIX } from './e2e-env';

// Core's first-run setup needs the bootstrap token (core.auth.setup_requires_token is true by
// default). The server writes it beside its database at boot, as .bootstrap-token, and
// playwright.config.ts starts the server with --db ${E2E_TMP_PREFIX}-db/e2e.pebble.
const BOOTSTRAP_TOKEN_FILE = join(`${E2E_TMP_PREFIX}-db`, '.bootstrap-token');

/**
 * GET /api/core/v1/me with the page's cookies: 200 and the named user means the session is real.
 * The SPA's own start-up burst (about 20 requests in a second) drains core's per-client rate
 * bucket (100 a minute, burst 20), so a 429 here is the limiter, not the session: poll past it.
 */
async function expectSignedInAs(page: Page, username: string) {
  let last: { status: number; username?: string } = { status: 0 };
  await expect
    .poll(
      async () => {
        const me = await page.request.get('/api/core/v1/me');
        const body = me.ok() ? ((await me.json()) as { user?: { username?: string } }) : undefined;
        last = { status: me.status(), username: body?.user?.username };
        return last.status;
      },
      { message: 'GET /api/core/v1/me with the browser session', timeout: 15000 }
    )
    .toBe(200);
  expect(last.username).toBe(username);
}

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

    await page.goto(paths.dashboard);
    await expect(page).toHaveURL(urlEndsWith(paths.login));
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

    await page.goto(paths.login);
    await expect(
      page.getByRole('heading', { name: 'Create Admin Account' })
    ).toBeVisible();

    await page.getByLabel('Username').fill('first-admin');
    await page.getByLabel('Email (optional)').fill('admin@example.com');
    await page.getByLabel('Password').fill('very-strong-password');
    await page.getByRole('button', { name: 'Create And Login' }).click();

    await expect(page).toHaveURL(urlEndsWith(paths.dashboard));
    // The logout control moved into UserMenu: the header now shows a Chip
    // labelled with the username (UserMenu.tsx:134-146) that opens a menu whose
    // Logout entry is a MenuItem, not a labelled button. Assert the same thing
    // the old locator stood for — the user is signed in and can sign out.
    const userChip = page.getByRole('button', { name: 'first-admin' });
    await expect(userChip).toBeVisible();
    await userChip.click();
    await expect(page.getByRole('menuitem', { name: 'Logout' })).toBeVisible();
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

    await page.goto(paths.login);
    await page.getByLabel('Username').fill('admin');
    await page.getByLabel('Password').fill('wrong-password');
    await page.getByRole('button', { name: 'Login' }).click();
    await expect(page.getByText('invalid credentials')).toBeVisible();

    await page.getByLabel('Password').fill('secretpass123');
    await page.getByRole('button', { name: 'Login' }).click();
    await expect(page).toHaveURL(urlEndsWith(paths.dashboard));
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
    const status = await request.get('/api/core/v1/auth/status');
    if (!status.ok()) {
      test.skip(true, 'Auth status endpoint unavailable — skipping real-server smoke test');
      return;
    }
    const body = await status.json() as { auth_disabled?: boolean; has_users?: boolean };
    const requiresAuth = !body.auth_disabled;
    if (!requiresAuth || body.has_users) {
      test.skip(
        true,
        `Server already bootstrapped (requires_auth=${requiresAuth}, has_users=${body.has_users}) — skip to avoid state pollution`
      );
      return;
    }

    // Fresh DB: exercise the real first-run path. Setup is refused (403 setup_closed) without the
    // bootstrap token, so the token the server wrote at boot is part of the flow, as an operator's is.
    if (!existsSync(BOOTSTRAP_TOKEN_FILE)) {
      const why = `no bootstrap token at ${BOOTSTRAP_TOKEN_FILE}: this server was not started by playwright.config.ts`;
      if (process.env.CI) throw new Error(why); // CI starts the server itself: a missing token is a failure
      test.skip(true, why);
      return;
    }
    const bootstrapToken = readFileSync(BOOTSTRAP_TOKEN_FILE, 'utf8').trim();

    await page.goto(paths.login);
    await expect(
      page.getByRole('heading', { name: 'Create Admin Account' })
    ).toBeVisible({ timeout: 10000 });

    await page.getByLabel('Username').fill('e2e-smoke-admin');
    await page.getByLabel(/Bootstrap token/).fill(bootstrapToken);
    await page.getByLabel('Password').fill('e2e-smoke-pass-9');
    await page.getByRole('button', { name: 'Create And Login' }).click();

    // Real session cookie must be set — the redirect only works with a valid cookie.
    await expect(page).toHaveURL(urlEndsWith(paths.dashboard), { timeout: 10000 });
    // The browser's own cookie jar now authenticates against core: /me answers the new admin.
    // (The header's user chip is not asserted here: a fresh server opens the first-run wizard
    // over the app, and a modal takes the header out of the accessibility tree.)
    await expectSignedInAs(page, 'e2e-smoke-admin');

    // Confirm the session survives a reload (cookie persists across navigation).
    await page.reload();
    await expect(page).toHaveURL(urlEndsWith(paths.dashboard));
    await expectSignedInAs(page, 'e2e-smoke-admin');
  });
});
