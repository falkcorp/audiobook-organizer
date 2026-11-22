// file: web/tests/e2e/stale-build-rollback.spec.ts
// version: 0.1.0
// guid: cb5f2a6c-9070-4d7f-b8d8-beee134fd628
// last-edited: 2026-11-22

// F9-03 (07 section 7.5.2 case 3, 07 section 7.18 "ABO e2e"): after a rollback to a
// release that predates X-App-Build, a tab holding the newer bundle sees app
// responses with `Reporting-Endpoints: csp=...` and no X-App-Build, and shows the
// banner. Responses without either (a proxy's 502, an Access interstitial) are
// not the app's and never trigger it.

import { expect, test, type Page } from '@playwright/test';
import { paths } from './utils/paths';

const STALE = /A new version is available\. Reload to use it\./;

/** Answers every API call as the pre-X-App-Build server would. */
async function rolledBackServer(page: Page, headers: Record<string, string>) {
  await page.route('**/api/v1/**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', headers, body: '{}' }),
  );
}

test.describe('stale build after a rollback (ABO bundle)', () => {
  test('shows the banner for an app response with no X-App-Build', async ({ page }) => {
    await rolledBackServer(page, { 'Reporting-Endpoints': 'csp="/api/v1/csp-report"' });
    await page.goto(paths.home);
    expect(await page.locator('meta[name="mediacore-build"]').getAttribute('content')).not.toBe('dev');
    await expect(page.getByText(STALE)).toBeVisible();
  });

  test('shows no banner for a response that is not the app\'s (no X-App-Build, no Reporting-Endpoints)', async ({ page }) => {
    await rolledBackServer(page, {});
    await page.goto(paths.home);
    await page.waitForLoadState('networkidle');
    await expect(page.getByText(STALE)).toHaveCount(0);
  });
});

test.describe('stale build after a rollback (shell bundle)', () => {
  test.skip(true, 'needs F9-03 commit 2 (main.tsx on createShell): the shell reads these headers on /api/core/v1 calls');

  test('shows the banner for a core response with no X-App-Build', async () => {
    // Drive with installCoreApiMock({ appBuild: '', headers: { 'Reporting-Endpoints': 'csp="/api/core/v1/csp-report"' } }).
  });
});
