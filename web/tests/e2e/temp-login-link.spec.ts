// file: web/tests/e2e/temp-login-link.spec.ts
// version: 0.1.0
// guid: 1f6c8e40-7a93-4d25-b0e7-3a9d5c2b8f61
// last-edited: 2026-11-21

// TestTempLoginLink_FragmentEndToEnd (07 section 7.18; F4-08's done-check, FE3-5,
// SEC2-6): an admin's reset or temp-login link is /login/temp#token=...; the
// SPA's /login/temp route reads the fragment, removes it with
// history.replaceState, posts the token in a body to
// POST /api/core/v1/auth/temp-login/consume and is signed in. The token never
// travels in a URL or a Referer, so a full page load carries it to no access log,
// trace or proxy. F4-05 owns the server half.
//
// Written against the post-switch SPA (the native /api/core/v1 identity routes
// through the generated client): it stays red until the SPA's switch (F4-08 B8
// proper) gives the legacy SPA its /login/temp route.

import { test, expect } from '@playwright/test';
import { installCoreAuthMock } from './utils/mockCoreAuth';
import { setupPhase2Interactive } from './utils/setup-modes';
import { legacyTempLoginLinkPath, tempLoginLinkPath } from './utils/paths';

const TOKEN = 'mcl_zatanna-zatara-temp-login-0001';
const USERNAME = 'bruce.wayne';

test.describe('Temp-login link (fragment form)', () => {
  test('signs the user in and removes the fragment with history.replaceState', async ({ page }) => {
    const mock = await installCoreAuthMock(page, { tempLogins: [{ token: TOKEN, username: USERNAME }] });
    await setupPhase2Interactive(page, undefined, {
      auth: { has_users: true, requires_auth: true, bootstrap_ready: false },
    });

    await page.goto(tempLoginLinkPath(TOKEN), { waitUntil: 'domcontentloaded' });

    // Signed in: the header shows the user chip (as auth-flow.spec.ts asserts).
    await expect(page.getByRole('button', { name: USERNAME })).toBeVisible();
    expect(mock.signedInAs()).toBe(USERNAME);

    // The fragment is gone from the address bar, removed by replaceState and
    // never by a push, and nothing re-added it.
    await expect.poll(() => page.evaluate(() => window.location.hash)).toBe('');
    expect(page.url()).not.toContain(TOKEN);
    const calls = await mock.historyCalls();
    const firstWrite = calls[0];
    expect(firstWrite?.fn).toBe('replaceState');
    expect(firstWrite?.url).not.toContain('token');
    expect(calls.some((c) => c.url.includes(TOKEN))).toBe(false);

    // The token went in one POST body, to the consume route, and nowhere else.
    const consumes = mock.requests.filter((r) => r.url.endsWith('/api/core/v1/auth/temp-login/consume'));
    expect(consumes).toHaveLength(1);
    expect(consumes[0].method).toBe('POST');
    expect(consumes[0].body).toEqual({ token: TOKEN });
    for (const r of mock.requests) {
      expect(r.url, `${r.method} ${r.url}`).not.toContain(TOKEN);
      expect(r.referer ?? '', `Referer of ${r.method} ${r.url}`).not.toContain(TOKEN);
    }
  });

  test('a spent or unknown token signs nobody in and still removes the fragment', async ({ page }) => {
    const mock = await installCoreAuthMock(page, { tempLogins: [{ token: TOKEN, username: USERNAME }] });
    await setupPhase2Interactive(page, undefined, {
      auth: { has_users: true, requires_auth: true, bootstrap_ready: false },
    });

    const stale = 'mcl_harvey-dent-spent-link-0002';
    await page.goto(tempLoginLinkPath(stale), { waitUntil: 'domcontentloaded' });

    await expect.poll(() => page.evaluate(() => window.location.hash)).toBe('');
    expect(mock.signedInAs()).toBeNull();
    await expect(page.getByRole('button', { name: USERNAME })).toHaveCount(0);
    for (const r of mock.requests) {
      expect(r.url, `${r.method} ${r.url}`).not.toContain(stale);
      expect(r.referer ?? '', `Referer of ${r.method} ${r.url}`).not.toContain(stale);
    }
  });

  test('the old query-string link form is not served as a sign-in', async ({ page }) => {
    const mock = await installCoreAuthMock(page, { tempLogins: [{ token: TOKEN, username: USERNAME }] });
    await setupPhase2Interactive(page, undefined, {
      auth: { has_users: true, requires_auth: true, bootstrap_ready: false },
    });

    // HEAD's GET /auth/temp-login?token= route is deleted (no 302, no window):
    // a link minted that way signs nobody in.
    await page.goto(legacyTempLoginLinkPath(TOKEN), { waitUntil: 'domcontentloaded' });
    expect(mock.signedInAs()).toBeNull();
    expect(mock.requests.some((r) => r.url.endsWith('/auth/temp-login/consume'))).toBe(false);
  });
});
