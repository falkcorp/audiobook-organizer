// file: web/tests/e2e/library-load-perf.spec.ts
// version: 1.1.0
// guid: 4b1c9d02-7ea6-4f31-9c8e-2d5a6f0b3e77
// last-edited: 2026-08-11

import { test, expect, type Page } from '@playwright/test';
import { generateTestBooks, setupLibraryWithBooks } from './utils/test-helpers';

/**
 * MEASUREMENT harness for "the library page freezes the browser on load".
 *
 * This file is a measuring instrument, not a gate. It is skipped unless
 * E2E_PERF=1 so it never runs in CI: the numbers are wall-clock and would be
 * flaky as assertions on a shared runner. The regression tests that DO gate
 * live in library-load-freeze.spec.ts and assert structure, not time.
 *
 * Three independent axes are measured, because "big page size" was the leading
 * hypothesis, not a finding:
 *
 *   A. page size      — N book cards rendered by one library load
 *   B. facet size     — how many authors/series the filter sidebar is handed on
 *                       mount (GET /authors and /series take no limit)
 *   C. soft-deleted   — how many soft-deleted rows Library.tsx pulls on mount
 *
 * Axis A is also run under CPU throttling. Every number here was taken on an
 * Apple-silicon laptop, which is far faster than the median machine a user
 * loads this page on; an unthrottled "2.5s" is not evidence that a user does
 * not see a freeze.
 *
 * Metrics per run:
 *   navMs        goto() -> first book card visible
 *   settleMs     goto() -> the expected content is in the DOM
 *   domNodes     document.getElementsByTagName('*').length after settle
 *   longTasks    count of PerformanceObserver 'longtask' entries
 *   blockingMs   sum of max(0, duration - 50) over those entries (TBT)
 *   maxTaskMs    longest single main-thread task
 */

interface Sample {
  label: string;
  navMs: number;
  settleMs: number;
  domNodes: number;
  longTasks: number;
  blockingMs: number;
  maxTaskMs: number;
}

const results: Sample[] = [];

/**
 * Install a longtask observer before any app code runs.
 *
 * 'longtask' is chromium-only; on webkit observe() throws and the counters stay
 * at zero, which is why the reported table is chromium's. The try/catch keeps
 * the run alive rather than failing the navigation on an unsupported type.
 */
async function instrument(page: Page, cpuThrottle = 1) {
  await page.addInitScript(() => {
    const w = window as unknown as {
      __perf: { count: number; blocking: number; max: number };
    };
    w.__perf = { count: 0, blocking: 0, max: 0 };
    try {
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          w.__perf.count += 1;
          w.__perf.blocking += Math.max(0, entry.duration - 50);
          w.__perf.max = Math.max(w.__perf.max, entry.duration);
        }
      }).observe({ entryTypes: ['longtask'] });
    } catch {
      /* not supported on this engine — counters stay zero */
    }
  });
  if (cpuThrottle > 1) {
    const client = await page.context().newCDPSession(page);
    await client.send('Emulation.setCPUThrottlingRate', { rate: cpuThrottle });
  }
}

async function readPerf(page: Page) {
  return page.evaluate(() => {
    const w = window as unknown as {
      __perf?: { count: number; blocking: number; max: number };
    };
    return {
      count: w.__perf?.count ?? 0,
      blocking: Math.round(w.__perf?.blocking ?? 0),
      max: Math.round(w.__perf?.max ?? 0),
      domNodes: document.getElementsByTagName('*').length,
    };
  });
}

/** Cards are located the same way library-browser.spec.ts locates them. */
function cardTitles(page: Page) {
  return page.getByRole('heading', { name: /^Test Book \d+$/ });
}

/**
 * Number of DOM nodes inside the soft-deleted panel.
 *
 * Deliberately counts the whole subtree rather than a MUI class: the rows are
 * rendered inside a collapsed <Collapse>, and the point of the measurement is
 * exactly that they exist there at all.
 */
async function softDeletedSubtreeNodes(page: Page): Promise<number> {
  return page.evaluate(() => {
    const heading = Array.from(document.querySelectorAll('h6')).find(
      (h) => h.textContent?.trim() === 'Soft-Deleted Books',
    );
    const panel = heading?.closest('.MuiPaper-root');
    return panel ? panel.getElementsByTagName('*').length : -1;
  });
}

test.describe('library load cost (measurement only)', () => {
  test.skip(process.env.E2E_PERF !== '1', 'set E2E_PERF=1 to run the measurement');
  // Deliberately serial: two workers measuring wall-clock on the same machine
  // measure each other.
  test.describe.configure({ mode: 'serial' });
  test.beforeEach(() => {
    test.setTimeout(300_000);
  });

  test.afterAll(() => {
    const header =
      '| case | nav ms | settle ms | DOM nodes | long tasks | blocking ms | max task ms |';
    const rule = '|---|---|---|---|---|---|---|';
    const rows = results.map(
      (r) =>
        `| ${r.label} | ${r.navMs} | ${r.settleMs} | ${r.domNodes} | ` +
        `${r.longTasks} | ${r.blockingMs} | ${r.maxTaskMs} |`,
    );
    console.log(['', 'MEASUREMENT TABLE', header, rule, ...rows, ''].join('\n'));
  });

  // --- Axis A: page size, at three CPU speeds -------------------------------
  const pageSizeCases: Array<[number, number]> = [
    [20, 1],
    [100, 1],
    [250, 1],
    [500, 1],
    [1000, 1],
    [20, 4], // control: the throttle alone must not look like a freeze
    [250, 4],
    [1000, 4],
    [1000, 6],
  ];
  for (const [n, throttle] of pageSizeCases) {
    test(`page size ${n} @${throttle}x`, async ({ page }) => {
      await instrument(page, throttle);
      await setupLibraryWithBooks(page, generateTestBooks(1200));

      const t0 = Date.now();
      await page.goto(`/library?limit=${n}`);
      await cardTitles(page).first().waitFor({ timeout: 240_000 });
      const navMs = Date.now() - t0;
      await expect
        .poll(() => cardTitles(page).count(), { timeout: 240_000 })
        .toBe(n);
      const settleMs = Date.now() - t0;

      const perf = await readPerf(page);
      results.push({
        label: `books=${n} @${throttle}x`,
        navMs,
        settleMs,
        domNodes: perf.domNodes,
        longTasks: perf.count,
        blockingMs: perf.blocking,
        maxTaskMs: perf.max,
      });
    });
  }

  // --- Axis B: facet size ----------------------------------------------------
  //
  // useLibraryFilters fires getAuthors()/getSeries() on mount and neither the
  // client nor GET /authors passes a limit, so production hands the page every
  // author and series row it has. Page size is pinned at the default 20 so
  // anything measured here is the facet cost alone.
  //
  // CAVEAT, and it is why the axis-B rows are not evidence that facet lists are
  // harmless in general: MUI's Autocomplete does not render option elements
  // until its listbox opens, so these rows measure the JSON parse plus the
  // map/filter/sort in useLibraryFilters, NOT the cost of rendering 50,000
  // options. That cost is real but it is paid on interaction, and the reported
  // bug is about load.
  for (const m of [5, 5_000, 50_000]) {
    test(`facets ${m}`, async ({ page }) => {
      await instrument(page);
      await setupLibraryWithBooks(page, generateTestBooks(40));

      // Registered AFTER setupLibraryWithBooks so it wins: Playwright matches
      // route handlers most-recently-registered first.
      await page.route('**/api/v1/authors', async (route) => {
        const items = Array.from({ length: m }, (_, i) => ({
          id: i + 1,
          name: `Author ${i + 1}`,
          book_count: (i % 7) + 1,
          aliases: [],
        }));
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: { items, count: m } }),
        });
      });
      await page.route('**/api/v1/series', async (route) => {
        const items = Array.from({ length: m }, (_, i) => ({
          id: i + 1,
          name: `Series ${i + 1}`,
          book_count: (i % 7) + 1,
        }));
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: { items, count: m } }),
        });
      });

      const t0 = Date.now();
      await page.goto('/library');
      await cardTitles(page).first().waitFor({ timeout: 240_000 });
      const navMs = Date.now() - t0;
      await expect
        .poll(() => cardTitles(page).count(), { timeout: 240_000 })
        .toBe(20);
      const settleMs = Date.now() - t0;

      const perf = await readPerf(page);
      results.push({
        label: `facets=${m} (payload only, listbox not rendered)`,
        navMs,
        settleMs,
        domNodes: perf.domNodes,
        longTasks: perf.count,
        blockingMs: perf.blocking,
        maxTaskMs: perf.max,
      });
    });
  }

  // --- Axis C: soft-deleted rows --------------------------------------------
  //
  // Library.tsx calls loadSoftDeleted() unconditionally on mount, which asks for
  // up to 10,000 rows, and LibrarySoftDeletedSection renders them inside a bare
  // MUI <Collapse>. Collapse does NOT unmount its children when closed, so every
  // one of those rows is built and inserted into the DOM on every library load
  // even though the section is collapsed by default and the user never sees it.
  //
  // Page size is pinned at the default 20, so a user who never touched the
  // items-per-page control still pays this in full. That is what makes it the
  // candidate that matches the report: it fires on load, unconditionally.
  for (const d of [0, 1_000, 2_000, 5_000, 10_000]) {
    test(`soft-deleted ${d}`, async ({ page }) => {
      await instrument(page);
      await setupLibraryWithBooks(page, generateTestBooks(40));

      await page.route('**/api/v1/audiobooks/soft-deleted*', async (route) => {
        const requested = Number(
          new URL(route.request().url()).searchParams.get('limit') ?? '100',
        );
        const items = Array.from(
          { length: Math.min(d, requested) },
          (_, i) => ({
            id: `deleted-${i + 1}`,
            title: `Deleted Book ${i + 1}`,
            author_name: `Author ${i % 50}`,
            file_path: `/library/deleted/book${i + 1}.m4b`,
            marked_for_deletion: true,
            marked_for_deletion_at: '2026-08-01T12:00:00Z',
          }),
        );
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            data: { items, total: d, count: items.length },
          }),
        });
      });

      const t0 = Date.now();
      await page.goto('/library');
      await cardTitles(page).first().waitFor({ timeout: 240_000 });
      const navMs = Date.now() - t0;
      await expect
        .poll(() => cardTitles(page).count(), { timeout: 240_000 })
        .toBe(20);
      // The soft-deleted fetch resolves after the first paint, so wait for the
      // count chip or the measurement misses the very cost it is aimed at.
      await expect(
        page.getByText(`${d} ${d === 1 ? 'item' : 'items'}`).first(),
      ).toBeVisible({ timeout: 240_000 });
      // Then let the (collapsed) rows finish committing.
      await page.waitForTimeout(3_000);
      const settleMs = Date.now() - t0;

      const perf = await readPerf(page);
      const panelNodes = await softDeletedSubtreeNodes(page);
      results.push({
        label: `softdeleted=${d} (collapsed panel subtree: ${panelNodes} nodes)`,
        navMs,
        settleMs,
        domNodes: perf.domNodes,
        longTasks: perf.count,
        blockingMs: perf.blocking,
        maxTaskMs: perf.max,
      });
    });
  }
});
