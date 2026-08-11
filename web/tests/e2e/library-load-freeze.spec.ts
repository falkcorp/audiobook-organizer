// file: web/tests/e2e/library-load-freeze.spec.ts
// version: 1.1.0
// guid: 9f2e4a71-6b3c-4d58-8e19-0c7a5b2d6f34
// last-edited: 2026-08-11

import { test, expect, type Page } from '@playwright/test';
import { generateTestBooks, setupLibraryWithBooks } from './utils/test-helpers';

/**
 * Regression gate for "the library page freezes the browser ON LOAD".
 *
 * Every assertion here is STRUCTURAL — a node count, a request parameter, a
 * rendered card count. None of them is a wall-clock budget, deliberately: the
 * wall-clock numbers that motivated these fixes live in library-load-perf.spec.ts
 * behind E2E_PERF=1, because a timing assertion on a shared CI runner degrades
 * into a flake that gets retried away, and this file has to be able to fail
 * loudly for years.
 *
 * What was measured on chromium before any code changed:
 *
 *   1. THE CAUSE. The soft-deleted panel pulled up to 10,000 rows on mount and
 *      rendered all of them inside a COLLAPSED MUI <Collapse>, which does not
 *      unmount its children. 141,061 DOM nodes and 8.0-10.8s of blocking at the
 *      DEFAULT page size of 20, entirely invisible. Expanding the panel
 *      afterwards changed the node count by ZERO, which is how "collapsed does
 *      not mean unrendered" was confirmed rather than assumed. This fires on
 *      load, for every user, whatever their settings — it matches the report.
 *
 *   2. A wasted request, NOT a freeze. A remembered page size of 1000 was
 *      reported as rendering 1000 cards forever. It does not: initial state
 *      seeds 1000, fires a 1000-row query, and the URL-sync effect (no
 *      localStorage fallback) overwrites it with 20 before anything paints.
 *      Traced 3/3 as requested limits ["1000", "20"] with 20 cards rendered.
 *      The cost is one superseded 1000-row query per load against a 366,922-
 *      book library.
 *
 *   3. Page size is real but only when ASKED for. 1,000 cards via an explicit
 *      ?limit=1000 is ~14,000ms to settle and ~8,700ms of blocked main thread at
 *      4x CPU throttle; 20 cards at the same throttle is 1,360ms / 278ms, so
 *      the throttle alone is not a freeze. That path is left working on
 *      purpose and is pinned below so it keeps working.
 */

/** Cards are located the same way library-browser.spec.ts locates them. */
function cardTitles(page: Page) {
  return page.getByRole('heading', { name: /^Test Book \d+$/ });
}

/**
 * DOM nodes inside the soft-deleted panel, found by its heading text.
 *
 * Deliberately NOT `getByTestId('soft-deleted-item')`. That test id is added by
 * the same commit as the fix, so a count-of-zero assertion on it would pass
 * against the broken code too — for the wrong reason, and this file would then
 * be a gate that has never been observed to fail. The heading and the Paper
 * around it predate the fix, so this measures the same thing in both versions.
 */
async function softDeletedPanelNodes(page: Page): Promise<number> {
  return page.evaluate(() => {
    const heading = Array.from(document.querySelectorAll('h6')).find(
      (h) => h.textContent?.trim() === 'Soft-Deleted Books',
    );
    const panel = heading?.closest('.MuiPaper-root');
    return panel ? panel.getElementsByTagName('*').length : -1;
  });
}

/**
 * Serve `total` soft-deleted books, honouring the client's requested `limit`.
 *
 * Honouring the limit is the point: the assertion is that the client stops
 * ASKING for ten thousand rows on mount, so a mock that ignored `limit` would
 * pass whatever the client did.
 */
async function mockSoftDeleted(page: Page, total: number, seen: number[]) {
  await page.route('**/api/v1/audiobooks/soft-deleted*', async (route) => {
    const limit = Number(
      new URL(route.request().url()).searchParams.get('limit') ?? '100',
    );
    seen.push(limit);
    const items = Array.from({ length: Math.min(total, limit) }, (_, i) => ({
      id: `deleted-${i + 1}`,
      title: `Deleted Book ${i + 1}`,
      author_name: `Author ${i % 50}`,
      file_path: `/library/deleted/book${i + 1}.m4b`,
      marked_for_deletion: true,
      marked_for_deletion_at: '2026-08-01T12:00:00Z',
    }));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: { items, total, count: items.length } }),
    });
  });
}

test.describe('library load does not build invisible work', () => {
  test('soft-deleted rows are not rendered while the panel is collapsed', async ({
    page,
  }) => {
    // GIVEN: 3,000 soft-deleted books — a plausible number for a library that
    // has been deduplicated, and 3,000x the ~14 nodes per row that made this a
    // freeze.
    const requestedLimits: number[] = [];
    await setupLibraryWithBooks(page, generateTestBooks(40));
    await mockSoftDeleted(page, 3000, requestedLimits);

    // WHEN: the user simply opens the library
    await page.goto('/library');
    await cardTitles(page).first().waitFor();
    // The count chip proves the soft-deleted response has been applied, so a
    // zero row count below cannot be "the fetch had not landed yet".
    await expect(page.getByText('3000 items').first()).toBeVisible();

    // THEN: the collapsed panel holds essentially nothing.
    //
    // Measured against the broken build with this exact mock: 42,017 nodes
    // (3,000 rows x ~14). Measured against the fixed build: 21 — the header,
    // its chip and the refresh button. 100 is a generous ceiling that leaves
    // room for the header to gain a control without becoming a tripwire, and
    // is still three orders of magnitude below the regression.
    expect(await softDeletedPanelNodes(page)).toBeLessThan(100);
    await expect(page.getByTestId('soft-deleted-item')).toHaveCount(0);
    await expect(page.getByTestId('soft-deleted-list')).toHaveCount(0);

    // AND: the mount request never asked for the rows in the first place.
    // Rendering none of 10,000 fetched rows would still ship a multi-megabyte
    // response and parse it on the main thread, so both halves are asserted.
    expect(requestedLimits.length).toBeGreaterThan(0);
    expect(Math.max(...requestedLimits)).toBeLessThanOrEqual(1);
  });

  test('expanding the soft-deleted panel loads a bounded page and says so', async ({
    page,
  }) => {
    const requestedLimits: number[] = [];
    await setupLibraryWithBooks(page, generateTestBooks(40));
    await mockSoftDeleted(page, 3000, requestedLimits);

    await page.goto('/library');
    await cardTitles(page).first().waitFor();
    await expect(page.getByText('3000 items').first()).toBeVisible();

    await page.getByText('Soft-Deleted Books').click();

    // Rows arrive, but capped — and the cap is stated on screen rather than
    // leaving a 500-row list sitting under a "3000 items" chip.
    await expect(page.getByTestId('soft-deleted-list')).toBeVisible();
    await expect(page.getByTestId('soft-deleted-item')).toHaveCount(500);
    await expect(
      page.getByText(/Showing the first 500 of 3,000 soft-deleted books/),
    ).toBeVisible();
    expect(Math.max(...requestedLimits)).toBeLessThanOrEqual(500);
  });
});

test.describe('library page size', () => {
  test('a remembered page size of 1000 does not issue a 1000-row query', async ({
    page,
  }) => {
    // GIVEN: a user who picked 1000 from the items-per-page dropdown at some
    // point in the past, so localStorage still holds it.
    //
    // The bug report said this renders 1000 cards on every later visit. It does
    // not — the URL-sync effect overwrites it with 20 before paint, and that is
    // true both before and after this fix. What the old code DID do is fire the
    // 1000-row query anyway and then throw the answer away. Against a library
    // of 366,922 books that is a real query on every load, so it is the thing
    // asserted here.
    const requestedLimits: string[] = [];
    page.on('request', (req) => {
      const url = new URL(req.url());
      if (!/\/api\/v1\/audiobooks\/?$/.test(url.pathname)) return;
      const limit = url.searchParams.get('limit');
      if (limit !== null) requestedLimits.push(limit);
    });

    await setupLibraryWithBooks(page, generateTestBooks(1200));
    await page.addInitScript(() => {
      // STORAGE_KEYS.LIBRARY_ITEMS_PER_PAGE — see web/src/lib/storageKeys.ts.
      localStorage.setItem('library_items_per_page', '1000');
    });

    // WHEN: they open the library with no ?limit in the URL
    await page.goto('/library');
    await cardTitles(page).first().waitFor();

    // THEN: 20 cards, same as before the fix — this is NOT a rendering change.
    await expect
      .poll(() => cardTitles(page).count(), { timeout: 30_000 })
      .toBe(20);

    // AND: the wasted query is gone. Pre-fix this array is ["1000", "20"];
    // post-fix it is ["20"]. Asserting on the whole array rather than the last
    // entry is the point — the old behaviour was invisible in the final state.
    expect(requestedLimits.length).toBeGreaterThan(0);
    expect(requestedLimits).not.toContain('1000');
  });

  test('an explicit ?limit=1000 is still honoured', async ({ page }) => {
    // The 1000 option is NOT removed. A user asking for it right now, in a URL
    // they can see, gets it — what was removed is the page silently deciding to
    // do it again on every future visit.
    await setupLibraryWithBooks(page, generateTestBooks(1200));

    await page.goto('/library?limit=1000');
    await cardTitles(page).first().waitFor();
    await expect
      .poll(() => cardTitles(page).count(), { timeout: 60_000 })
      .toBe(1000);
  });

  test('an out-of-range ?limit is clamped to the maximum', async ({ page }) => {
    // Library.tsx's URL-sync effect runs on the first commit, and its own limit
    // parser had a lower bound but no upper one — so a hand-edited or shared
    // link could push itemsPerPage past the ceiling the initial-state clamp
    // exists to enforce, on load.
    await setupLibraryWithBooks(page, generateTestBooks(1200));

    await page.goto('/library?limit=50000');
    await cardTitles(page).first().waitFor();
    await expect
      .poll(() => cardTitles(page).count(), { timeout: 60_000 })
      .toBe(1000);
  });
});
