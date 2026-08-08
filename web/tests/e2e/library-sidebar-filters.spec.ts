// file: web/tests/e2e/library-sidebar-filters.spec.ts
// version: 1.0.0
// guid: 3c9a71e5-84fd-4b26-a0d7-6f2e5b81c934
// last-edited: 2026-08-08

import { test, expect, type Page } from '@playwright/test';

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
 * #2193 fixed both, but only with unit tests. These tests exercise the real
 * click path so a regression fails CI rather than being reported by the owner.
 */

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
 * The sidebar sub-item button.
 *
 * Filtered on exact text rather than `getByRole('button', { name })`: MUI's
 * ListItemButton wraps the label in a nested element alongside an icon, so the
 * computed accessible name does not match the label exactly.
 */
function sidebarItem(page: Page, name: string) {
  return page.getByRole('button').filter({ hasText: new RegExp(`^${name}$`) });
}

test.describe('Library sidebar filters', () => {
  test('clicking In Progress moves the highlight off All Books', async ({ page }) => {
    await setupLibraryWithBooks(page, generateTestBooks(5));
    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // GIVEN: a plain /library visit selects All Books.
    await expect(sidebarItem(page, 'All Books')).toHaveClass(/Mui-selected/);
    await expect(sidebarItem(page, 'In Progress')).not.toHaveClass(/Mui-selected/);

    // WHEN: the user clicks In Progress.
    await sidebarItem(page, 'In Progress').click();
    await page.waitForLoadState('networkidle');

    // THEN: the highlight actually moves. Before #2193 the comparison was
    // against location.pathname, so All Books stayed lit forever.
    await expect(sidebarItem(page, 'In Progress')).toHaveClass(/Mui-selected/);
    await expect(sidebarItem(page, 'All Books')).not.toHaveClass(/Mui-selected/);
  });

  test('clicking In Progress survives the URL settling with page=1', async ({ page }) => {
    await setupLibraryWithBooks(page, generateTestBooks(5));
    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    await sidebarItem(page, 'In Progress').click();
    await page.waitForLoadState('networkidle');

    // Library.tsx re-encodes the colon and appends page=1 once its effects
    // settle. The filter must survive that rewrite — the stuck guard used to
    // throw the search away and restore a bare page=1.
    await expect(page).toHaveURL(/[?&]search=read_status(%3A|:)in_progress/);

    // Decoded comparison, mirroring how the sidebar decides selection.
    const search = new URL(page.url()).searchParams.get('search');
    expect(search).toBe('read_status:in_progress');
  });

  test('clicking In Progress sends the filter to the server', async ({ page }) => {
    const requests = recordBookRequests(page);
    await setupLibraryWithBooks(page, generateTestBooks(5));
    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    const before = requests.length;
    await sidebarItem(page, 'In Progress').click();
    await page.waitForLoadState('networkidle');

    // THEN: a NEW list request went out carrying the read_status filter.
    // This is the half of the bug the user described as "not actually
    // filtering or adding any filter" — the click used to be a pure no-op,
    // so no request with a filter was ever issued.
    const after = requests.slice(before);
    expect(after.length).toBeGreaterThan(0);
    const filtered = after.filter((u) => {
      const filters = new URL(u).searchParams.get('filters');
      return !!filters && filters.includes('read_status') && filters.includes('in_progress');
    });
    expect(filtered.length).toBeGreaterThan(0);
  });

  test('Finished is fixed by the same change', async ({ page }) => {
    await setupLibraryWithBooks(page, generateTestBooks(5));
    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    await sidebarItem(page, 'Finished').click();
    await page.waitForLoadState('networkidle');

    await expect(sidebarItem(page, 'Finished')).toHaveClass(/Mui-selected/);
    await expect(sidebarItem(page, 'All Books')).not.toHaveClass(/Mui-selected/);
    expect(new URL(page.url()).searchParams.get('search')).toBe('read_status:finished');
  });

  test('All Books clears the filter and takes the highlight back', async ({ page }) => {
    await setupLibraryWithBooks(page, generateTestBooks(5));
    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    await sidebarItem(page, 'In Progress').click();
    await page.waitForLoadState('networkidle');
    await expect(sidebarItem(page, 'In Progress')).toHaveClass(/Mui-selected/);

    // All Books navigates with ?reset=1, which Library.tsx handles BEFORE its
    // echo guard — the reason it kept working even while the others were dead.
    await sidebarItem(page, 'All Books').click();
    await page.waitForLoadState('networkidle');

    await expect(sidebarItem(page, 'All Books')).toHaveClass(/Mui-selected/);
    await expect(sidebarItem(page, 'In Progress')).not.toHaveClass(/Mui-selected/);
    expect(new URL(page.url()).searchParams.get('search')).toBeNull();
  });
});
