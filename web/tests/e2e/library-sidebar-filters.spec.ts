// file: web/tests/e2e/library-sidebar-filters.spec.ts
// version: 1.2.0
// guid: 3c9a71e5-84fd-4b26-a0d7-6f2e5b81c934
// last-edited: 2026-08-09

import { test, expect, type Page, type Locator } from '@playwright/test';

import { generateTestBooks, setupLibraryWithBooks } from './utils/test-helpers';

/**
 * Regression coverage for the Library sidebar filter items (#2193).
 *
 * Two independent bugs made "In Progress" and "Finished" dead controls, and
 * both were invisible to the existing suite:
 *
 *   1. The selected highlight compared `location.pathname` against a path that
 *      carries a query string, so "In Progress" could never match while
 *      "All Books" matched on every /library URL — the highlight was pinned.
 *   2. A stuck `isInternalUpdate` guard in Library.tsx discarded the incoming
 *      `search` param and rewrote the URL back, so the click applied nothing.
 *
 * #2193 fixed both, but only with unit tests. These exercise the real click
 * path so a regression fails CI instead of being reported by the owner.
 *
 * NOTE for anyone running this locally: `playwright.config.ts` sets
 * `reuseExistingServer: !process.env.CI`, so a stray server on 127.0.0.1:8484
 * will be reused and you will silently test whatever bundle IT was built from.
 * Confirm with `ps -o lstart -p $(lsof -ti :8484)` or kill it first.
 */

/** The Library sub-item buttons, which live inside the collapsible group. */
function subItems(page: Page): Locator {
  return page.locator('.MuiCollapse-root:visible .MuiListItemButton-root');
}

/**
 * Whichever sub-items are currently highlighted.
 *
 * Asserting on this set rather than on each item's class is deliberate: it
 * states the real invariant — exactly one item is lit, and it is the right
 * one — and it does not depend on resolving an individual button, which MUI
 * makes awkward (the label sits in a nested node, so the computed accessible
 * name does not equal the label).
 *
 * `:visible` and the `.MuiCollapse-root` scope are both load-bearing. Sidebar
 * renders its content TWICE — once for the temporary (mobile) Drawer and once
 * for the permanent one — so an unscoped `.Mui-selected` matches five elements
 * on a plain /library visit: ["Library", "All Books", "", "Library",
 * "All Books"]. Scoping to the Collapse drops the parent "Library" item, which
 * is highlighted by pathname and is not one of these filters; `:visible` drops
 * the offscreen duplicate drawer.
 */
function selectedSubItems(page: Page): Locator {
  return page.locator('.MuiCollapse-root:visible .MuiListItemButton-root.Mui-selected');
}

function clickSubItem(page: Page, name: string) {
  return subItems(page).filter({ hasText: new RegExp(`^${name}$`) }).click();
}

/** Records every audiobooks list request the page issues. */
function recordBookRequests(page: Page): string[] {
  const urls: string[] = [];
  page.on('request', (req) => {
    const u = new URL(req.url());
    if (u.pathname === '/api/v1/audiobooks') urls.push(req.url());
  });
  return urls;
}

async function openLibrary(page: Page) {
  await setupLibraryWithBooks(page, generateTestBooks(5));
  await page.goto('/library');
  await page.waitForLoadState('networkidle');
}

test.describe('Library sidebar filters', () => {
  test('a plain /library visit highlights All Books alone', async ({ page }) => {
    await openLibrary(page);
    await expect(selectedSubItems(page)).toHaveText(['All Books']);
  });

  test('clicking In Progress moves the highlight off All Books', async ({ page }) => {
    await openLibrary(page);
    await expect(selectedSubItems(page)).toHaveText(['All Books']);

    await clickSubItem(page, 'In Progress');

    // Before #2193 the comparison was against location.pathname, so All Books
    // stayed lit forever and In Progress could never match.
    await expect(selectedSubItems(page)).toHaveText(['In Progress']);
  });

  test('clicking In Progress survives the URL settling with page=1', async ({ page }) => {
    await openLibrary(page);
    await clickSubItem(page, 'In Progress');

    // Library.tsx re-encodes the colon and appends page=1 once its effects
    // settle. The filter must survive that rewrite — the stuck guard used to
    // throw the search away and restore a bare page=1.
    // Poll rather than read once. Library does NOT settle monotonically — it
    // writes the search, then briefly rewrites a bare `page=1` with the search
    // GONE, then writes the encoded form back (verified by sampling
    // location.search every frame; see the fixme at the bottom of this file).
    // A single synchronous read lands in that gap often enough to fail ~40% of
    // webkit runs, and `toHaveURL` passing first does not help — it matches the
    // pre-settle state and the gap opens after it.
    await expect
      .poll(() => new URL(page.url()).searchParams.get('search'))
      .toBe('read_status:in_progress');
  });

  test('clicking In Progress sends the filter to the server', async ({ page }) => {
    const requests = recordBookRequests(page);
    await openLibrary(page);

    const before = requests.length;
    await clickSubItem(page, 'In Progress');
    await expect(page).toHaveURL(/search=read_status(%3A|:)in_progress/);

    // The half the owner described as "not actually filtering or adding any
    // filter": the click used to be a pure no-op, so no filtered request was
    // ever issued.
    await expect
      .poll(() =>
        requests.slice(before).filter((u) => {
          const f = new URL(u).searchParams.get('filters');
          return !!f && f.includes('read_status') && f.includes('in_progress');
        }).length
      )
      .toBeGreaterThan(0);
  });

  test('Finished is fixed by the same change', async ({ page }) => {
    await openLibrary(page);
    await clickSubItem(page, 'Finished');

    await expect(selectedSubItems(page)).toHaveText(['Finished']);
    // Same transient-drop hazard as the In Progress test above — poll, do not
    // read once. Waiting on the highlight is not a gate: the highlight and the
    // search param are written by different effects.
    await expect
      .poll(() => new URL(page.url()).searchParams.get('search'))
      .toBe('read_status:finished');
  });

  // KNOWN DEFECT, deliberately not routed around. Sampling `location.search`
  // every animation frame across a sidebar click shows Library.tsx passing
  // through a state where it has THROWN THE FILTER AWAY, 3 runs out of 3:
  //
  //     ?page=1                                 (initial)
  //     ?search=read_status:finished            (click applies the filter)
  //     ?page=1                                 <-- filter gone
  //     ?search=read_status%3Afinished&page=1   (re-applied, settled)
  //
  // This is the same shape the comment on the "survives the URL settling" test
  // above describes as fixed by #2193 — the stuck guard "used to throw the
  // search away and restore a bare page=1". It still happens; it just recovers
  // now, so nothing downstream notices.
  //
  // Measured blast radius, so nobody re-derives it: exactly ONE request reaches
  // /api/v1/audiobooks after the click and it carries the correct `filters`
  // param — the transient drop costs no wasted query and never sends the server
  // the wrong thing. The cost is confined to the URL and history: a spurious
  // intermediate entry, and a flicker for anything reading searchParams
  // directly. That is why this is filed rather than treated as urgent.
  //
  // Marked `fixme` (skipped, never executed) rather than `fail`. The race is
  // real but not certain: forced on, this check fails **5 runs out of 8** on
  // webkit. `test.fail()` would therefore report a spurious "expected to fail
  // but passed" on roughly a third of runs, which is a worse signal than none.
  // To reproduce deliberately:
  //
  //   sed -i '' "s/test.fixme('the filter never/test('the filter never/" \
  //     tests/e2e/library-sidebar-filters.spec.ts
  //   npx playwright test -c tests/e2e/playwright.config.ts --project=webkit \
  //     tests/e2e/library-sidebar-filters.spec.ts -g "never disappears" \
  //     --repeat-each=8 --workers=1
  //
  // When the race is actually fixed this should pass 8/8; flip it to a normal
  // test at that point.
  //
  // See todo.d/20260809-library-url-transient-filter-drop.md.
  test.fixme('the filter never disappears from the URL while the effects settle', async ({ page }) => {
    await openLibrary(page);

    await page.evaluate(() => {
      const w = window as unknown as { __urls: string[] };
      w.__urls = [];
      const tick = () => {
        w.__urls.push(location.search);
        if (w.__urls.length < 3000) requestAnimationFrame(tick);
      };
      tick();
    });

    await clickSubItem(page, 'Finished');
    await expect
      .poll(() => new URL(page.url()).searchParams.get('search'))
      .toBe('read_status:finished');

    const samples: string[] = await page.evaluate(
      () => (window as unknown as { __urls: string[] }).__urls
    );
    const firstSet = samples.findIndex((u) => u.includes('read_status'));
    expect(firstSet, 'the filter should reach the URL at all').toBeGreaterThanOrEqual(0);
    const droppedAfterBeingSet = samples
      .slice(firstSet)
      .filter((u) => !u.includes('read_status'));
    expect(
      droppedAfterBeingSet,
      'once applied, the filter must never leave the URL again'
    ).toEqual([]);
  });

  test('All Books clears the filter and takes the highlight back', async ({ page }) => {
    await openLibrary(page);
    await clickSubItem(page, 'In Progress');
    await expect(selectedSubItems(page)).toHaveText(['In Progress']);

    // All Books navigates with ?reset=1, which Library.tsx handles BEFORE its
    // echo guard — the reason it kept working even while the others were dead.
    await clickSubItem(page, 'All Books');

    await expect(selectedSubItems(page)).toHaveText(['All Books']);
    await expect
      .poll(() => new URL(page.url()).searchParams.get('search'))
      .toBeNull();
  });
});
