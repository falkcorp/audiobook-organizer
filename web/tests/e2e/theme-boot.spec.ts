// file: web/tests/e2e/theme-boot.spec.ts
// version: 0.1.0
// guid: a3d2e978-f9cc-4d81-95fb-8540c4c6c9be
// last-edited: 2026-11-22

// F9-03: the colour scheme is set before the first paint, by the page's one inline
// boot script, so a dark-mode load never flashes the default scheme. The script is
// ABO's hand-written one in web/index.html until commit 1, and the one the
// mediacore() preset writes (from ABO's themeStorageKey and defaultMode) after it;
// every test here holds on both sides of that commit.
//
// The app's own bundle is blocked in each test, so React never mounts and MUI's
// ThemeProvider cannot be what sets the attribute: only the boot script can.

import { expect, test, type Page } from '@playwright/test';
import { paths } from './utils/paths';

const KEY = 'app-theme-mode';

async function loadWithoutApp(page: Page, saved: string | null) {
  await page.route('**/assets/**', (route) => (route.request().resourceType() === 'script' ? route.abort() : route.continue()));
  if (saved !== null) await page.addInitScript(([k, v]) => localStorage.setItem(k, v), [KEY, saved]);
  await page.goto(paths.home);
}

test.describe('theme boot script', () => {
  test('applies a saved light scheme before React mounts', async ({ page }) => {
    await loadWithoutApp(page, 'light');
    await expect(page.locator('html')).toHaveAttribute('data-mui-color-scheme', 'light');
    await expect(page.locator('#root')).toBeEmpty();
  });

  test('applies a saved dark scheme before React mounts', async ({ page }) => {
    await loadWithoutApp(page, 'dark');
    await expect(page.locator('html')).toHaveAttribute('data-mui-color-scheme', 'dark');
  });

  test('is dark when nothing is saved', async ({ page }) => {
    await loadWithoutApp(page, null);
    await expect(page.locator('html')).toHaveAttribute('data-mui-color-scheme', 'dark');
  });

  test('is dark for a saved value it does not know', async ({ page }) => {
    await loadWithoutApp(page, 'sepia');
    await expect(page.locator('html')).toHaveAttribute('data-mui-color-scheme', 'dark');
  });

  test('exactly one inline script runs in the head, and it is the boot script', async ({ page }) => {
    await loadWithoutApp(page, null);
    const inline = page.locator('head script:not([src])');
    await expect(inline).toHaveCount(1);
    expect(await inline.textContent()).toContain(KEY);
  });

  test('the page carries the build ID meta the server reads back', async ({ page }) => {
    await loadWithoutApp(page, null);
    const content = await page.locator('meta[name="mediacore-build"]').getAttribute('content');
    expect(content).toMatch(/^[A-Za-z0-9._-]{1,64}$/);
    expect(content).not.toBe('dev');
  });
});

// The preset also writes <meta name="mediacore-app"> and <meta name="mediacore-theme">.
// Before commit 1 the page has neither, so these two wait for it.
test.describe('theme boot script: the preset metas', () => {
  test.skip(true, 'needs F9-03 commit 1 (vite preset): index.html has no mediacore-app or mediacore-theme meta before it');

  test('names the app and the theme key and default', async ({ page }) => {
    await loadWithoutApp(page, null);
    await expect(page.locator('meta[name="mediacore-app"]')).toHaveAttribute('content', 'abo');
    await expect(page.locator('meta[name="mediacore-theme"]')).toHaveAttribute('content', `${KEY} dark`);
  });
});
