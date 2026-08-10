// file: web/tests/e2e/library-sidebar-filters.spec.ts
// version: 1.3.0
// guid: 3c9a71e5-84fd-4b26-a0d7-6f2e5b81c934
// last-edited: 2026-08-10

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

/**
 * Records every query string the app publishes, by intercepting the history
 * API rather than sampling.
 *
 * The first version of these tests sampled `location.search` on
 * requestAnimationFrame. That instrument is not sensitive enough to state the
 * invariant: rAF fires at ~16ms, and the whole drop-and-restore sequence can
 * happen between two frames — most easily on the mount commit, where the
 * offending write lands within a millisecond or two of hydration. A sampling
 * test that cannot see the bug passes whether or not the bug is there.
 *
 * react-router writes through pushState/replaceState, so wrapping both
 * observes every URL the app ever publishes, with no timing assumptions.
 *
 * Must be installed with addInitScript, not evaluate: some of these writes
 * happen on the very first commit, before a post-goto evaluate could run.
 */
function installUrlRecorder(page: Page) {
  return page.addInitScript(() => {
    const w = window as unknown as { __urls: string[] };
    w.__urls = [];
    const record = (url?: string | URL | null) => {
      w.__urls.push(url == null ? location.search : new URL(String(url), location.href).search);
    };
    const origPush = history.pushState.bind(history);
    const origReplace = history.replaceState.bind(history);
    history.pushState = (d: unknown, t: string, url?: string | URL | null) => {
      origPush(d, t, url as string);
      record(url);
    };
    history.replaceState = (d: unknown, t: string, url?: string | URL | null) => {
      origReplace(d, t, url as string);
      record(url);
    };
    // The URL the document loaded with, which no history call reports.
    record();
  });
}

/** Every query string published so far, oldest first. */
function recordedUrls(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { __urls: string[] }).__urls);
}

/**
 * Asserts `token` never leaves the URL once it has appeared. Takes the
 * recorded history rather than a live locator so the assertion covers
 * intermediate states the user's browser really passed through.
 */
function expectNeverDropped(samples: string[], token: string) {
  const firstSet = samples.findIndex((u) => u.includes(token));
  expect(firstSet, `${token} should reach the URL at all (saw: ${JSON.stringify(samples)})`)
    .toBeGreaterThanOrEqual(0);
  expect(
    samples.slice(firstSet).filter((u) => !u.includes(token)),
    `once applied, ${token} must never leave the URL again`
  ).toEqual([]);
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

  // Was `test.fixme` while the defect it describes was open. FIXED and now a
  // normal test — this is the regression guard for it.
  //
  // The defect: across a sidebar click, Library.tsx passed through a state
  // where it had THROWN THE FILTER AWAY.
  //
  //     ?page=1                                 (initial)
  //     ?search=read_status:finished            (click applies the filter)
  //     ?page=1                                 <-- filter gone
  //     ?search=read_status%3Afinished&page=1   (re-applied, settled)
  //
  // Same shape the "survives the URL settling" test above describes as fixed
  // by #2193 — the stuck guard "used to throw the search away and restore a
  // bare page=1". #2193 fixed one cause; this was a second, independent one
  // (effect ordering, see the write effect in Library.tsx).
  //
  // Measured blast radius, so nobody re-derives it: exactly ONE request reached
  // /api/v1/audiobooks after the click and it carried the correct `filters`
  // param — the transient drop cost no wasted query and never sent the server
  // the wrong thing. The cost was confined to the URL and history: a spurious
  // intermediate entry, and a flicker for anything reading searchParams
  // directly. That is why it was filed rather than treated as urgent.
  //
  // It is a race, so it was never certain: 5 failures in 8 runs on webkit when
  // it was open. Verified fixed at 24 consecutive passes, and the assertion
  // verified still able to fail by disabling the guard (4 failures in 6). Both
  // numbers matter — a green race test proves nothing until you have watched
  // it go red.
  test('the filter never disappears from the URL while the effects settle', async ({ page }) => {
    await installUrlRecorder(page);
    await openLibrary(page);

    await clickSubItem(page, 'Finished');
    await expect
      .poll(() => new URL(page.url()).searchParams.get('search'))
      .toBe('read_status:finished');

    expectNeverDropped(await recordedUrls(page), 'read_status');
  });

  // The same invariant reached by the OTHER entry path. The test above always
  // navigates from an already-mounted Library, so the URL-write guard has a
  // previous query string to compare against. A deep link (bookmark, shared
  // URL, reload) has none: the filter is present on the very first commit, so
  // the guard is structurally blind there and the write effect's mount run is
  // unguarded.
  //
  // Both deep-link tests below PASS with the guard removed — measured, by
  // disabling it and re-running: 4 of 6 click runs failed, 6 of 6 deep-link
  // runs passed. They are not regression coverage for a bug that was fixed;
  // they pin an invariant that currently holds for a different reason, namely
  // that every URL-backed field is seeded from `searchParams` in its useState
  // initialiser (Library.tsx `initialSearch`/`initialSortBy`/…,
  // useLibraryFilters.ts `filters`), so mount state is not stale to begin
  // with. Add a URL param without a seeded initialiser and the mount write
  // starts dropping it — that is what these would catch.
  test('a deep-linked filter survives mount', async ({ page }) => {
    await setupLibraryWithBooks(page, generateTestBooks(5));
    await installUrlRecorder(page);

    await page.goto('/library?search=read_status:finished');
    await expect
      .poll(() => new URL(page.url()).searchParams.get('search'))
      .toBe('read_status:finished');

    expectNeverDropped(await recordedUrls(page), 'read_status');
  });

  // Same invariant, `?tag=` instead of `?search=`, and deliberately kept as a
  // second case: `selectedTags` is the one URL-backed field NOT seeded from
  // searchParams (useLibraryFilters.ts, `useState<string[]>([])`), so on paper
  // it is the field a mount-time write should drop. It does not — verified
  // with the guard disabled, 6 of 6 — so something else covers it. Kept
  // because the reasoning says it is the most fragile field, and a cheap test
  // on the most fragile field is worth keeping even when it is green.
  test('a deep-linked tag survives mount', async ({ page }) => {
    await setupLibraryWithBooks(page, generateTestBooks(5));
    await installUrlRecorder(page);

    await page.goto('/library?tag=scifi');
    await expect.poll(() => new URL(page.url()).searchParams.get('tag')).toBe('scifi');

    expectNeverDropped(await recordedUrls(page), 'tag=scifi');
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
