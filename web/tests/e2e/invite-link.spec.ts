// file: web/tests/e2e/invite-link.spec.ts
// version: 0.1.0
// guid: 6d0b4f93-2c7e-4a18-8e55-9b1a3d7c4e02
// last-edited: 2026-11-21

// TestInviteLink_FragmentEndToEnd (07 section 7.18; F4-08's done-check, FE4-2,
// FE5-4, SEC2-6): an invite minted on the build in which core auth decides
// requests is /invite#token=...; the SPA's /invite route reads the fragment,
// removes it with history.replaceState, looks the invite up with
// POST /api/core/v1/auth/invites/lookup (token in the body) and accepts it with
// POST /api/core/v1/auth/invites/accept. Opened signed out it shows the invite
// page and accepting signs the invitee in; opened signed in it shows the invite
// page, not NotFound. The token never travels in a URL or a Referer.
//
// Written against the post-switch SPA: it stays red until the SPA's switch
// (F4-08 B8 proper) gives the legacy SPA its fragment-reading /invite route
// (today's route is the path form /invite/:token).

import { test, expect, type Page } from '@playwright/test';
import { installCoreAuthMock, type CoreAuthMock } from './utils/mockCoreAuth';
import { setupPhase2Interactive } from './utils/setup-modes';

const TOKEN = 'mci_selina-kyle-invite-link-0001';
const INVITE = { token: TOKEN, username: 'dick.grayson', role: 'viewer' };

async function boot(page: Page, signedInAs?: string): Promise<CoreAuthMock> {
  const mock = await installCoreAuthMock(page, { invites: [INVITE], signedInAs });
  await setupPhase2Interactive(page, undefined, {
    auth: { has_users: true, requires_auth: true, bootstrap_ready: false },
  });
  return mock;
}

function expectTokenOnlyInBodies(mock: CoreAuthMock) {
  for (const r of mock.requests) {
    expect(r.url, `${r.method} ${r.url}`).not.toContain(TOKEN);
    expect(r.referer ?? '', `Referer of ${r.method} ${r.url}`).not.toContain(TOKEN);
  }
}

async function expectFragmentRemoved(page: Page, mock: CoreAuthMock) {
  await expect.poll(() => page.evaluate(() => window.location.hash)).toBe('');
  expect(page.url()).not.toContain(TOKEN);
  const calls = await mock.historyCalls();
  expect(calls[0]?.fn).toBe('replaceState');
  expect(calls[0]?.url).not.toContain('token');
  expect(calls.some((c) => c.url.includes(TOKEN))).toBe(false);
}

test.describe('Invite link (fragment form)', () => {
  test('opened signed out: shows the invite, accepting it signs the invitee in', async ({ page }) => {
    const mock = await boot(page);

    await page.goto(`/invite#token=${TOKEN}`, { waitUntil: 'domcontentloaded' });

    // The invite page, with what the lookup returned.
    await expect(page.getByText(INVITE.username)).toBeVisible();
    await expectFragmentRemoved(page, mock);

    await page.getByLabel('Choose a password').fill('a-long-enough-password');
    await page.getByLabel('Confirm password').fill('a-long-enough-password');
    await page.getByRole('button', { name: /accept|create|join/i }).click();

    await expect(page.getByRole('button', { name: INVITE.username })).toBeVisible();
    expect(mock.signedInAs()).toBe(INVITE.username);

    const lookups = mock.requests.filter((r) => r.url.endsWith('/api/core/v1/auth/invites/lookup'));
    expect(lookups).toHaveLength(1);
    expect(lookups[0].body).toEqual({ token: TOKEN });
    const accepts = mock.requests.filter((r) => r.url.endsWith('/api/core/v1/auth/invites/accept'));
    expect(accepts).toHaveLength(1);
    expect(accepts[0].body).toEqual({ token: TOKEN, password: 'a-long-enough-password' });
    expectTokenOnlyInBodies(mock);
  });

  test('opened signed in: still shows the invite page, not NotFound', async ({ page }) => {
    const mock = await boot(page, 'bruce.wayne');

    await page.goto(`/invite#token=${TOKEN}`, { waitUntil: 'domcontentloaded' });

    await expect(page.getByText(INVITE.username)).toBeVisible();
    await expect(page.getByLabel('Choose a password')).toBeVisible();
    await expect(page.getByRole('heading', { name: /not found/i })).toHaveCount(0);
    await expectFragmentRemoved(page, mock);
    expectTokenOnlyInBodies(mock);
  });

  test('an unknown or spent token shows the invalid-link answer and no form', async ({ page }) => {
    const mock = await boot(page);

    const stale = 'mci_oswald-cobblepot-spent-invite-0002';
    await page.goto(`/invite#token=${stale}`, { waitUntil: 'domcontentloaded' });

    await expect(page.getByLabel('Choose a password')).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.location.hash)).toBe('');
    expect(mock.signedInAs()).toBeNull();
    for (const r of mock.requests) {
      expect(r.url, `${r.method} ${r.url}`).not.toContain(stale);
      expect(r.referer ?? '', `Referer of ${r.method} ${r.url}`).not.toContain(stale);
    }
  });

  test('the old path-form link opens no invite', async ({ page }) => {
    const mock = await boot(page);

    // An /invite/<token> link minted before the flip: no lookup, no form.
    await page.goto(`/invite/${TOKEN}`, { waitUntil: 'domcontentloaded' });

    await expect(page.getByLabel('Choose a password')).toHaveCount(0);
    expect(mock.requests.some((r) => r.url.endsWith('/auth/invites/lookup'))).toBe(false);
    expect(mock.signedInAs()).toBeNull();
  });
});
