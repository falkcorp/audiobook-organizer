// file: core/web/e2e/stale-build.spec.ts
// version: 0.1.0
// guid: 9e4b1d70-2f63-4a85-b7c9-5d0a8e3f6b12
// last-edited: 2026-11-20

// stale-build.spec.ts, core half (07 §7.18, §7.5.2): the server's X-App-Build
// equals the page's mediacore-build meta, so no banner shows; when an API
// response carries a different build (a deploy happened under the tab) the
// non-dismissable reload banner shows. The mismatch is produced by rewriting
// the header of the real response in the browser: one hello binary cannot
// serve two builds.

import { expect, test } from '@playwright/test';

test('the served build equals the meta, and no banner shows', async ({ page }) => {
  const res = await page.goto('/login');
  const meta = await page.locator('meta[name="mediacore-build"]').getAttribute('content');
  expect(meta).toBeTruthy();
  expect(meta).not.toBe('dev');
  expect(res?.headers()['x-app-build']).toBe(meta);
  const api = await page.request.get('/api/core/v1/auth/status');
  expect(api.headers()['x-app-build']).toBe(meta);
  await page.waitForLoadState('networkidle');
  await expect(page.getByTestId('stale-build-banner')).toHaveCount(0);
});

test('a different X-App-Build on an API response shows the reload banner', async ({ page }) => {
  await page.route('**/api/**', async (route) => {
    const response = await route.fetch();
    await route.fulfill({ response, headers: { ...response.headers(), 'x-app-build': 'joker-9999999999' } });
  });
  await page.goto('/login');
  const banner = page.getByTestId('stale-build-banner');
  await expect(banner).toBeVisible();
  await expect(banner).toHaveAttribute('data-reason', 'header-mismatch');
  await expect(banner.getByRole('button', { name: 'Reload' })).toBeVisible();
  await expect(banner.getByRole('button', { name: /close|dismiss/i })).toHaveCount(0);
});
