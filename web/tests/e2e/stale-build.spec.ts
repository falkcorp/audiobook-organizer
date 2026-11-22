// file: web/tests/e2e/stale-build.spec.ts
// version: 0.1.0
// guid: 344fdd08-911a-4b1f-8820-2b132f1714c8
// last-edited: 2026-11-22

// F9-03 (07 section 7.5.2 case 1, 07 section 7.18 "ABO e2e"): a tab whose bundle is
// older than the server shows the stale-build banner and keeps working. The server
// stamps every response with X-App-Build, the build ID of the bundle it serves;
// the page read its own ID once from <meta name="mediacore-build">. The ABO half
// is the legacy bundle (apiFetch); the shell half waits for commit 2.

import { expect, test } from '@playwright/test';
import { paths } from './utils/paths';

const STALE = /A new version is available\. Reload to use it\./;

/** Serves the real index.html, but claiming a build the server no longer has. */
async function claimBuild(page: import('@playwright/test').Page, id: string) {
  await page.route(
    (url) => url.pathname === paths.home,
    async (route) => {
      const response = await route.fetch();
      const html = (await response.text()).replace(
        /(<meta\b[^>]*\bname="mediacore-build"[^>]*\bcontent=")[^"]*(")/i,
        `$1${id}$2`,
      );
      await route.fulfill({ response, body: html });
    },
  );
}

test.describe('stale build (ABO bundle)', () => {
  test('shows the banner when X-App-Build differs from the page\'s build ID', async ({ page }) => {
    await claimBuild(page, 'batcave-old-1');
    await page.goto(paths.home);
    await expect(page.locator('meta[name="mediacore-build"]')).toHaveAttribute('content', 'batcave-old-1');
    await expect(page.getByText(STALE)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Reload' })).toBeVisible();
  });

  test('the server stamps the page with an X-App-Build equal to its build ID meta', async ({ page }) => {
    // The server reads the ID back out of the embedded index.html, so this fails if it cannot
    // parse the tag the build wrote (the preset's tag after F9-03 commit 1).
    const response = await page.goto(paths.home);
    const header = response?.headers()['x-app-build'];
    const meta = await page.locator('meta[name="mediacore-build"]').getAttribute('content');
    expect(meta).not.toBe('dev');
    expect(header).toBe(meta);
  });

  test('shows no banner when the page\'s build ID is the server\'s', async ({ page }) => {
    await page.goto(paths.home);
    const id = await page.locator('meta[name="mediacore-build"]').getAttribute('content');
    expect(id).not.toBe('dev');
    // Let the first API calls answer, then check the banner never came up.
    await page.waitForLoadState('networkidle');
    await expect(page.getByText(STALE)).toHaveCount(0);
  });

  test('a page built as dev is never stale', async ({ page }) => {
    await claimBuild(page, 'dev');
    await page.goto(paths.home);
    await page.waitForLoadState('networkidle');
    await expect(page.getByText(STALE)).toHaveCount(0);
  });
});

test.describe('stale build (shell bundle)', () => {
  test.skip(true, 'needs F9-03 commit 2 (main.tsx on createShell) and commit 3 (UIBundle): the shell checks /api/core/v1/modules too (case 2, legacy-spa)');

  test('shows the banner when /api/core/v1/modules says the server serves a legacy SPA', async () => {
    // Drive with installCoreApiMock({ app: { uiBuildId: '' }, appBuild: <the page's build ID> }).
  });
});
