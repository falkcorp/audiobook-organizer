// file: web/tests/e2e/library-empty-state.spec.ts
// version: 1.0.0
// guid: 8f52c0d6-3b47-49ae-9c81-7a0e6d2b5143
// last-edited: 2026-08-08

import { test, expect, type Page } from '@playwright/test';

import { generateTestBooks, setupLibraryWithBooks } from './utils/test-helpers';

/**
 * Regression coverage for the Library empty-state fix (#2195).
 *
 * Restarting the backend made the Library announce "No Audiobooks Found" to
 * someone with a 44,000-book library. `useLibraryQuery` answered a failed load
 * by calling `setAudiobooks([])` — actively discarding what was on screen —
 * and its `finally` block set `loading` false. `LibraryBookGrid` then chose its
 * empty state with `audiobooks.length === 0 && !loading && !searchQuery`, with
 * no error branch and no error prop to consult, so a failure produced exactly
 * the state that condition matches.
 *
 * Not an edge case: the backend refuses connections for ~40s after every deploy
 * while memdb warms over the library.
 *
 * These tests drive the real failure path with a forced 503 rather than
 * restarting a server, which Playwright's managed `webServer` will not do.
 *
 * NOTE when running locally: `playwright.config.ts` sets
 * `reuseExistingServer: !process.env.CI`, so a stray server on 127.0.0.1:8484
 * is reused and you will silently test whatever bundle IT was built from.
 * Check with `ps -o lstart -p $(lsof -ti :8484)` or kill it first.
 */

const EMPTY_STATE = 'No Audiobooks Found';
const RECONNECTING = 'Loading your library';

function bookRequestCount(page: Page): () => number {
  let n = 0;
  page.on('request', (req) => {
    if (new URL(req.url()).pathname === '/api/v1/audiobooks') n += 1;
  });
  return () => n;
}

test.describe('Library empty state', () => {
  test('a failed load never claims the library is empty', async ({ page }) => {
    // GIVEN: the books endpoint is down, exactly as during memdb warmup.
    await setupLibraryWithBooks(page, generateTestBooks(5), {
      failures: { getBooks: 503 },
    });

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // THEN: the user is told we are still loading, NOT that they own nothing.
    await expect(page.getByText(RECONNECTING)).toBeVisible();
    await expect(page.getByText(EMPTY_STATE)).toHaveCount(0);
  });

  test('a failed load keeps retrying on its own', async ({ page }) => {
    const count = bookRequestCount(page);
    await setupLibraryWithBooks(page, generateTestBooks(5), {
      failures: { getBooks: 503 },
    });

    await page.goto('/library');
    await expect(page.getByText(RECONNECTING)).toBeVisible();

    // The backoff starts at 500ms and caps at 5s, so several attempts land
    // inside this window. Before #2195 the first failure was terminal: the
    // list was emptied, loading went false, and nothing ever tried again
    // without a manual refresh.
    const first = count();
    await expect.poll(() => count(), { timeout: 15000 }).toBeGreaterThan(first);
  });

  test('a genuinely empty library still says so', async ({ page }) => {
    // The fix must not swing the other way: a load that SUCCEEDS with zero
    // results is the one case that may legitimately claim emptiness, and
    // first-run users depend on the guidance in that panel.
    await setupLibraryWithBooks(page, []);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    await expect(page.getByText(EMPTY_STATE)).toBeVisible();
    await expect(page.getByText(RECONNECTING)).toHaveCount(0);
  });

  test('a healthy library shows neither panel', async ({ page }) => {
    await setupLibraryWithBooks(page, generateTestBooks(5));

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    await expect(page.getByText(EMPTY_STATE)).toHaveCount(0);
    await expect(page.getByText(RECONNECTING)).toHaveCount(0);
  });
});
