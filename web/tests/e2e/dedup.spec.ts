// file: web/tests/e2e/dedup.spec.ts
// version: 1.3.0
// guid: d1e2f3a4-b5c6-7d8e-9f0a-1b2c3d4e5f6a
// last-edited: 2026-08-09

import { test, expect, type Page } from '@playwright/test';
import {
  setupMockApi,
  generateTestBooks,
  type MockAuthorDedupGroup,
  type MockSeriesDupGroup,
} from './utils/test-helpers';

// ── Fixtures ──

const authorDedupGroups: MockAuthorDedupGroup[] = [
  {
    canonical: { id: 1, name: 'Brandon Sanderson' },
    variants: [{ id: 2, name: 'Brandon  Sanderson' }, { id: 3, name: 'B. Sanderson' }],
    book_count: 12,
    suggested_name: 'Brandon Sanderson',
  },
  {
    canonical: { id: 4, name: 'James S. A. Corey' },
    variants: [{ id: 5, name: 'James S.A. Corey' }],
    book_count: 9,
    suggested_name: 'James S. A. Corey',
  },
  {
    canonical: { id: 10, name: 'R.A. Mejia Charles Dean' },
    variants: [],
    book_count: 3,
    split_names: ['R. A. Mejia', 'Charles Dean'],
  },
  {
    canonical: { id: 20, name: 'Graphic Audio' },
    variants: [],
    book_count: 5,
    is_production_company: true,
  },
  {
    canonical: { id: 21, name: 'Soundbooth Theater' },
    variants: [],
    book_count: 8,
    is_production_company: true,
  },
];

const seriesDedupGroups: MockSeriesDupGroup[] = [
  {
    canonical: { id: 100, name: 'The Stormlight Archive' },
    variants: [{ id: 101, name: 'Stormlight Archive' }, { id: 102, name: 'Stormlight' }],
    book_count: 4,
  },
  {
    canonical: { id: 200, name: 'Foundation' },
    variants: [{ id: 201, name: 'The Foundation Series' }],
    book_count: 7,
  },
];

const booksForAuthor = [
  { id: 'b1', title: 'The Way of Kings', author_name: 'Brandon Sanderson', cover_url: null },
  { id: 'b2', title: 'Words of Radiance', author_name: 'Brandon Sanderson', cover_url: '/api/v1/covers/local/abc.jpg' },
  { id: 'b3', title: 'Oathbringer', author_name: 'Brandon Sanderson', cover_url: null },
];

/**
 * Opt into the legacy tabbed Dedup UI.
 *
 * /dedup was redesigned: it now renders a unified candidate view, and the
 * tabbed Books/Authors/Series UI these tests cover sits behind a "Legacy View"
 * toggle persisted in sessionStorage. Without this, `?tab=authors` renders the
 * unified surface and every tab assertion fails with "element(s) not found".
 *
 * Must be called BEFORE page.goto — every test that navigates to /dedup needs
 * it, including the ones that call page.goto directly rather than going
 * through openDedupPage().
 *
 * The unified view itself is NOT covered by this spec; see the todo fragment
 * filed alongside this change.
 */
async function enableLegacyDedupView(page: Page) {
  await page.addInitScript(() => {
    try {
      sessionStorage.setItem('dedup_show_legacy', '1');
    } catch {
      /* storage disabled — the test fails loudly rather than silently */
    }
  });
}

async function openDedupPage(page: Page, opts: {
  authorGroups?: MockAuthorDedupGroup[];
  seriesGroups?: MockSeriesDupGroup[];
  books?: ReturnType<typeof generateTestBooks>;
  tab?: 'authors' | 'series' | 'books';
} = {}) {
  const books = opts.books || generateTestBooks(5);

  await setupMockApi(page, {
    books,
    authorDedup: { groups: opts.authorGroups ?? authorDedupGroups },
    seriesDedup: { groups: opts.seriesGroups ?? seriesDedupGroups, total_series: 50 },
  });

  // Mock the author books endpoint for popover
  await page.route('**/api/v1/audiobooks?author_id=*', async (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items: booksForAuthor, count: booksForAuthor.length }),
    });
  });

  await enableLegacyDedupView(page);

  const tab = opts.tab ?? 'authors';
  await page.goto(`/dedup?tab=${tab}`);
  await page.waitForLoadState('networkidle');
}

// ── Author Dedup Tests ──

test.describe('Author Dedup', () => {
  test('displays author duplicate groups', async ({ page }) => {
    await openDedupPage(page);

    // Use heading role to target canonical name specifically
    await expect(page.getByRole('heading', { name: 'Brandon Sanderson' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'James S. A. Corey' })).toBeVisible();
  });

  test('shows variant count and book count chips', async ({ page }) => {
    await openDedupPage(page);

    await expect(page.getByText('12 book(s)')).toBeVisible();
    await expect(page.getByText('9 book(s)')).toBeVisible();
  });

  test('shows variant chips', async ({ page }) => {
    await openDedupPage(page);

    await expect(page.getByText('B. Sanderson').first()).toBeVisible();
    await expect(page.getByText('James S.A. Corey').first()).toBeVisible();
  });

  test('shows composite author split info', async ({ page }) => {
    await openDedupPage(page);

    await expect(page.getByText('R. A. Mejia').first()).toBeVisible();
    await expect(page.getByText('Charles Dean').first()).toBeVisible();
    await expect(page.getByText(/split into/i).first()).toBeVisible();
  });

  test('production company groups have orange badge', async ({ page }) => {
    await openDedupPage(page);

    const prodChips = page.getByText('Production Company');
    await expect(prodChips.first()).toBeVisible();
    expect(await prodChips.count()).toBe(2);
  });

  test('production company group has Find Real Author button', async ({ page }) => {
    await openDedupPage(page);

    const findButtons = page.getByRole('button', { name: /find real author/i });
    await expect(findButtons.first()).toBeVisible();
    expect(await findButtons.count()).toBe(2);
  });

  test('clicking Find Real Author triggers API call', async ({ page }) => {
    await openDedupPage(page);

    // Track whether the resolve-production endpoint was called
    let resolveCallCount = 0;
    await page.route('**/api/v1/authors/*/resolve-production', async (route) => {
      resolveCallCount++;
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          operation: { id: 'resolve-prod-1', type: 'resolve-production-author', status: 'completed', progress: 100, total: 100, message: 'Done' },
        }),
      });
    });

    // Also mock the status polling to return completed immediately
    await page.route('**/api/v1/operations/*/status', async (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'resolve-prod-1', type: 'resolve-production-author', status: 'completed', progress: 100, total: 100, message: 'Done',
        }),
      });
    });

    // Arm the waiter BEFORE clicking. waitForRequest only observes requests
    // made after it is called, and with the route above mocked the request
    // fires and completes essentially instantly — so a waiter set up after the
    // click could never see it. This was a race in the test, not app drift.
    const resolveRequest = page.waitForRequest(req => req.url().includes('/resolve-production'));

    await page.getByRole('button', { name: /find real author/i }).first().click();

    await resolveRequest;
    expect(resolveCallCount).toBeGreaterThan(0);
  });

  test('merge button merges variant authors into canonical', async ({ page }) => {
    await openDedupPage(page);

    const mergeBtn = page.getByRole('button', { name: /merge into "brandon sanderson"/i });
    await expect(mergeBtn).toBeVisible();

    await mergeBtn.click();

    // Should show success feedback
    await expect(page.getByText(/merged|success/i).first()).toBeVisible({ timeout: 5000 });
  });

  test('split button splits composite author', async ({ page }) => {
    await openDedupPage(page);

    const splitBtn = page.getByRole('button', { name: /split into 2 authors/i });
    await expect(splitBtn).toBeVisible();

    await splitBtn.click();

    // Should show success
    await expect(page.getByText(/split|updated|success/i).first()).toBeVisible({ timeout: 5000 });
  });

  test('edit canonical name inline', async ({ page }) => {
    await openDedupPage(page);

    // Find edit button (pencil icon next to canonical name)
    const editBtns = page.locator('button').filter({ has: page.locator('[data-testid="EditIcon"]') });
    await editBtns.first().click();

    // Should show text field in main content area (not the search bar)
    const input = page.getByRole('main').getByRole('textbox');
    await expect(input).toBeVisible();
    await expect(input).toHaveValue('Brandon Sanderson');
  });

  test('empty state when no duplicate groups', async ({ page }) => {
    await openDedupPage(page, { authorGroups: [] });

    await expect(page.getByText(/no duplicate|no author/i).first()).toBeVisible();
  });
});

// ── Book Preview Popover Tests ──

test.describe('Book Preview Popover', () => {
  test('clicking book count chip opens popover with books', async ({ page }) => {
    await openDedupPage(page);

    await page.getByText('12 book(s)').click();

    await expect(page.getByText('The Way of Kings')).toBeVisible();
    await expect(page.getByText('Words of Radiance')).toBeVisible();
    await expect(page.getByText('Oathbringer')).toBeVisible();
  });

  test('popover shows cover thumbnails when available', async ({ page }) => {
    await openDedupPage(page);

    await page.getByText('12 book(s)').click();

    // Words of Radiance has a cover_url
    const img = page.locator('img[src*="covers/local"]');
    await expect(img).toBeVisible();
  });

  test('popover shows books without covers', async ({ page }) => {
    await openDedupPage(page);

    await page.getByText('12 book(s)').click();

    // Books without covers should still appear
    await expect(page.getByText('The Way of Kings')).toBeVisible();
  });

  test('clicking book in popover navigates to book detail', async ({ page }) => {
    await openDedupPage(page);

    await page.getByText('12 book(s)').click();
    await page.getByText('The Way of Kings').click();

    await expect(page).toHaveURL(/\/library\/b1/);
  });

  test('popover closes when clicking outside', async ({ page }) => {
    await openDedupPage(page);

    await page.getByText('12 book(s)').click();
    await expect(page.getByText('The Way of Kings')).toBeVisible();

    // Click outside the popover
    await page.mouse.click(10, 10);

    await expect(page.getByText('The Way of Kings')).not.toBeVisible();
  });

  test('popover handles empty book list', async ({ page }) => {
    await setupMockApi(page, {
      books: generateTestBooks(5),
      authorDedup: { groups: authorDedupGroups },
    });
    await page.route('**/api/v1/audiobooks?author_id=*', async (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [], count: 0 }),
      });
    });

    await enableLegacyDedupView(page);
    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    await page.getByText('12 book(s)').click();
    await expect(page.getByText(/no books/i).first()).toBeVisible();
  });
});

// ── Series Dedup Tests ──

test.describe('Series Dedup', () => {
  test('displays series duplicate groups', async ({ page }) => {
    await openDedupPage(page, { tab: 'series' });

    // Verify the series tab content loads without error
    // The exact rendering depends on the SeriesDedupTab component
    const seriesTab = page.getByRole('tab', { name: /series/i });
    await expect(seriesTab).toHaveAttribute('aria-selected', 'true');
  });

  test('empty state when no series duplicates', async ({ page }) => {
    await openDedupPage(page, { tab: 'series', seriesGroups: [] });

    // Should show empty state or the series tab content
    const seriesTab = page.getByRole('tab', { name: /series/i });
    await expect(seriesTab).toHaveAttribute('aria-selected', 'true');
  });
});

// ── Tab Navigation Tests ──

test.describe('Dedup Tab Navigation', () => {
  test('default tab is Books when no query param', async ({ page }) => {
    await setupMockApi(page, {
      books: generateTestBooks(3),
      authorDedup: { groups: authorDedupGroups },
    });
    await enableLegacyDedupView(page);
    await page.goto('/dedup');
    await page.waitForLoadState('networkidle');

    // TAB_NAMES[0] is still 'books', but its LABEL is now "Version Groups".
    // /books/i matched "Split Books" — a different, correctly-unselected tab —
    // so this assertion was checking the wrong element entirely.
    const booksTab = page.getByRole('tab', { name: /version groups/i });
    await expect(booksTab).toHaveAttribute('aria-selected', 'true');
  });

  test('can switch between tabs', async ({ page }) => {
    await openDedupPage(page);

    // Should be on Authors tab
    await expect(page.getByRole('heading', { name: 'Brandon Sanderson' })).toBeVisible();

    // Switch to Books
    await page.getByRole('tab', { name: /books/i }).click();
    // Books tab should become active
    const booksTab = page.getByRole('tab', { name: /books/i });
    await expect(booksTab).toHaveAttribute('aria-selected', 'true');

    // Switch back to Authors
    await page.getByRole('tab', { name: /authors/i }).click();
    await expect(page.getByRole('heading', { name: 'Brandon Sanderson' })).toBeVisible();
  });

  test('tabs are visible', async ({ page }) => {
    await openDedupPage(page);

    await expect(page.getByRole('tab', { name: /books/i })).toBeVisible();
    await expect(page.getByRole('tab', { name: /authors/i })).toBeVisible();
    await expect(page.getByRole('tab', { name: /series/i })).toBeVisible();
  });
});

// ── Refresh & Operations ──

test.describe('Dedup Refresh Operations', () => {
  test('refresh button is visible on authors tab', async ({ page }) => {
    await openDedupPage(page);

    // There should be a refresh button
    const refreshBtn = page.locator('button').filter({ has: page.locator('[data-testid="RefreshIcon"]') }).or(
      page.getByRole('button', { name: /refresh/i })
    );
    await expect(refreshBtn.first()).toBeVisible();
  });
});

// ── Pagination ──

test.describe('Dedup Pagination', () => {
  test('paginates author groups', async ({ page }) => {
    const manyGroups: MockAuthorDedupGroup[] = Array.from({ length: 30 }, (_, i) => ({
      canonical: { id: i + 100, name: `Author ${i + 1}` },
      variants: [{ id: i + 200, name: `Auth ${i + 1}` }],
      book_count: i + 1,
    }));

    await openDedupPage(page, { authorGroups: manyGroups });

    // Should show at least the first author (exact match to avoid matching Author 10-19)
    await expect(page.getByRole('heading', { name: 'Author 1', exact: true })).toBeVisible();

    // Look for pagination controls
    const paginationEl = page.locator('[class*="Pagination"], [aria-label*="pagination"], [class*="TablePagination"]');
    if (await paginationEl.count() > 0) {
      await expect(paginationEl.first()).toBeVisible();
    }
  });
});

// ── Bulk Actions ──

test.describe('Dedup Bulk Actions', () => {
  test('merge all button is visible', async ({ page }) => {
    await openDedupPage(page);

    const mergeAllBtn = page.getByRole('button', { name: /merge all/i });
    await expect(mergeAllBtn).toBeVisible();
  });

  test('merge all button opens confirmation dialog', async ({ page }) => {
    await openDedupPage(page);

    const mergeAllBtn = page.getByRole('button', { name: /merge all/i });
    await mergeAllBtn.click();

    // Confirmation dialog
    await expect(page.getByRole('heading', { name: /confirm/i })).toBeVisible();
  });
});

// ── AI Review ──

test.describe('Dedup AI Review', () => {
  test('AI Review button is visible on authors tab', async ({ page }) => {
    await openDedupPage(page);

    // AI Review is a Tab in the legacy view (role="tab"), not a button.
    const aiTab = page.getByRole('tab', { name: /ai review/i });
    await expect(aiTab).toBeVisible();
  });
});
