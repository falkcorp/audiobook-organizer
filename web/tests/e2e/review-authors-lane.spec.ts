// file: web/tests/e2e/review-authors-lane.spec.ts
// version: 1.5.1
// guid: 3f9a1d68-2c74-4b05-8e13-a7d06c5b9e42
// last-edited: 2026-10-16

// End-to-end coverage for the Authors & series lane of /review (Authors and Series
// sub-views).
//
// Ported from the blocks of dedup.spec.ts and dedup-operations.spec.ts that drove
// the Authors tab of /dedup: Author Dedup, Book Preview Popover, Dedup Refresh
// Operations, Dedup Pagination, Dedup Bulk Actions, Production Company
// Resolution, Dedup Operation Progress and Dedup Error Handling. The old blocks
// stay where they are until the /dedup page is deleted.
//
// What changed in the port:
//   - the route is /review?lane=authors, not /dedup?tab=authors;
//   - progress is asserted through the toast and the notification-bell entry, not
//     the page-local progress bar, because the lane has no page-local poll;
//   - the error cases assert the four-state rule: a failed read is an error with
//     Retry, never an empty list;
//   - fixtures are synthetic names ("Author A"), not real library rows.
//
// The Series block is the port of dedup.spec.ts "Series Dedup", which
// only asserted that a tab was selected; this one drives the sub-view. Its API is
// mocked in this file (openSeries) rather than in test-helpers, whose series
// fixture has no author ids, no match types and no merge or rename routes.
//
// The AI scans block at the end is the port of dedup.spec.ts "Dedup AI Review",
// which only asserted that a tab was visible; this one drives the sub-view and the
// lane menu's Start / Cancel commands. Its API is mocked in this file (openAI).

import { test, expect, type Page } from '@playwright/test';
import { setupMockApi, generateTestBooks, type MockAuthorDedupGroup } from './utils/test-helpers';

// ── Fixtures ──

const authorGroups: MockAuthorDedupGroup[] = [
  {
    canonical: { id: 1, name: 'Author A' },
    variants: [
      { id: 2, name: 'Author  A' },
      { id: 3, name: 'A. Author' },
    ],
    book_count: 12,
    suggested_name: 'Author A Preferred',
  },
  {
    canonical: { id: 4, name: 'Author B Example' },
    variants: [{ id: 5, name: 'Author B. Example' }],
    book_count: 9,
  },
  {
    canonical: { id: 10, name: 'Author C Author D' },
    variants: [],
    book_count: 3,
    split_names: ['Author C', 'Author D'],
  },
  {
    canonical: { id: 20, name: 'Studio One' },
    variants: [],
    book_count: 5,
    is_production_company: true,
  },
  {
    canonical: { id: 21, name: 'Studio Two' },
    variants: [],
    book_count: 8,
    is_production_company: true,
  },
];

const booksForAuthor = [
  { id: 'b1', title: 'Book One', author_name: 'Author A', cover_url: null },
  { id: 'b2', title: 'Book Two', author_name: 'Author A', cover_url: '/api/v1/covers/local/abc.jpg' },
  { id: 'b3', title: 'Book Three', author_name: 'Author A', cover_url: null },
];

async function openLane(
  page: Page,
  opts: {
    groups?: MockAuthorDedupGroup[];
    books?: unknown[];
    needsRefresh?: boolean;
  } = {}
) {
  await setupMockApi(page, {
    books: generateTestBooks(5),
    authorDedup: { groups: opts.groups ?? authorGroups, needs_refresh: opts.needsRefresh },
    operations: { active: [], history: [], logs: {} },
  });
  await page.route('**/api/v1/audiobooks?author_id=*', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items: opts.books ?? booksForAuthor, count: (opts.books ?? booksForAuthor).length }),
    });
  });
  await page.goto('/review?lane=authors');
  await expect(page.getByTestId('authors-panel')).toBeVisible();
}

/** Registers a handler that counts the requests matching `pattern` and returns the counter. */
async function countRequests(page: Page, pattern: string | RegExp, method: string) {
  const seen = { n: 0 };
  page.on('request', (req) => {
    if (req.method() === method && (typeof pattern === 'string' ? req.url().includes(pattern) : pattern.test(req.url()))) {
      seen.n++;
    }
  });
  return seen;
}


/** An OperationV2 row, as GET /operations/timeline serves it to the notification bell. */
function bellRow(id: string, defId: string, displayName: string): Record<string, unknown> {
  return {
    id,
    def_id: defId,
    plugin: 'dedup',
    display_name: displayName,
    status: 'running',
    priority: 0,
    notify_level: 0,
    progress_current: 10,
    progress_total: 100,
    progress_message: displayName,
    current_phase: null,
    current_item: null,
    actor_user_id: null,
    parent_id: null,
    queued_at: '2026-10-13T10:00:00Z',
    started_at: '2026-10-13T10:00:01Z',
    completed_at: null,
    error_message: null,
    resume_count: 0,
    trace_id: null,
    span_id: null,
  };
}

/**
 * Makes the notification bell list `row`, but only AFTER a POST to `trigger` has
 * been requested. The bell reads GET /operations/timeline; seeding the row up
 * front would show it whether or not the click did anything, so an assertion on
 * the bell would prove nothing about the action under test.
 *
 * The bell learns about a running operation from the server's event stream and
 * from the timeline it loads when the app starts. The mock has no event stream,
 * so a test reloads the page after the action to make the bell read the timeline
 * again (`showBell` below).
 */
async function bellListsAfter(page: Page, trigger: string, row: Record<string, unknown>) {
  let started = false;
  page.on('request', (req) => {
    if (req.method() === 'POST' && req.url().includes(trigger)) started = true;
  });
  await page.route('**/api/v1/operations/timeline*', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: { operations: started ? [row] : [] } }),
    });
  });
}

/** Reloads (so the bell re-reads the timeline) and opens the bell. */
async function showBell(page: Page) {
  await page.reload();
  await expect(page.getByTestId('authors-panel')).toBeVisible();
  await page.getByTestId('operations-bell').click();
}

// ── The lane ──

test.describe('Authors lane', () => {
  test('is the fourth lane, before Repairs, and opens from ?lane=authors', async ({ page }) => {
    await openLane(page);
    const tabs = page.getByRole('tab');
    await expect(tabs).toHaveText(['Duplicates', 'Metadata', 'Review queue', 'Authors & series', 'Repairs']);
    await expect(page.getByRole('tab', { name: 'Authors & series' })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByTestId('authors-subview-authors')).toHaveText('Authors');
  });
});

// ── Author Dedup ──

test.describe('Author Dedup', () => {
  test('displays author duplicate groups', async ({ page }) => {
    await openLane(page);
    await expect(page.getByRole('heading', { name: 'Author A', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Author B Example' })).toBeVisible();
  });

  test('shows book count chips', async ({ page }) => {
    await openLane(page);
    await expect(page.getByText('12 credit(s)')).toBeVisible();
    await expect(page.getByText('9 credit(s)')).toBeVisible();
  });

  test('shows variant chips and the suggested name', async ({ page }) => {
    await openLane(page);
    await expect(page.getByText('A. Author').first()).toBeVisible();
    await expect(page.getByText('Author B. Example').first()).toBeVisible();
    await expect(page.getByText('Author A Preferred')).toBeVisible();
  });

  test('shows composite author split info', async ({ page }) => {
    await openLane(page);
    await expect(page.getByText('Author C', { exact: true })).toBeVisible();
    await expect(page.getByText('Author D', { exact: true })).toBeVisible();
    await expect(page.getByText(/split into/i).first()).toBeVisible();
  });

  test('production company groups carry a badge', async ({ page }) => {
    await openLane(page);
    const chips = page.getByText('Production Company');
    await expect(chips.first()).toBeVisible();
    expect(await chips.count()).toBe(2);
  });

  test('production company groups have a Find Real Author button', async ({ page }) => {
    await openLane(page);
    const buttons = page.getByRole('button', { name: /find real author/i });
    await expect(buttons.first()).toBeVisible();
    expect(await buttons.count()).toBe(2);
  });

  test('clicking Find Real Author calls the resolve endpoint', async ({ page }) => {
    await openLane(page);
    await page.route('**/api/v1/authors/*/resolve-production', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          operation: { id: 'resolve-prod-1', type: 'resolve-production-author', status: 'completed', progress: 100, total: 100 },
        }),
      });
    });
    // Armed BEFORE the click: waitForRequest only sees requests made after it is
    // called, and with the route mocked the request completes almost instantly.
    const resolveRequest = page.waitForRequest((req) => req.url().includes('/resolve-production'));
    await page.getByRole('button', { name: /find real author/i }).first().click();
    await resolveRequest;
  });

  test('the merge button merges variants into the canonical author', async ({ page }) => {
    await openLane(page);
    const merged = page.waitForRequest((req) => req.url().endsWith('/api/v1/authors/merge'));
    await page.getByRole('button', { name: 'Merge into "Author A"', exact: true }).click();
    const request = await merged;
    expect(request.postDataJSON()).toEqual({ keep_id: 1, merge_ids: [2, 3] });
    await expect(page.getByText('Merged author(s) into "Author A"')).toBeVisible();
    // The group left the list.
    await expect(page.getByRole('heading', { name: 'Author A', exact: true })).toHaveCount(0);
  });

  test('removing a variant keeps it out of the merge', async ({ page }) => {
    await openLane(page);
    await page.getByRole('button', { name: 'Remove "A. Author" from this merge' }).click();
    const merged = page.waitForRequest((req) => req.url().endsWith('/api/v1/authors/merge'));
    await page.getByRole('button', { name: 'Merge into "Author A"', exact: true }).click();
    expect((await merged).postDataJSON()).toEqual({ keep_id: 1, merge_ids: [2] });
  });

  test('toggling a variant to Narrator reclassifies it, then merges the rest', async ({ page }) => {
    await openLane(page);
    const reclassified = page.waitForRequest((req) => req.url().includes('/authors/2/reclassify-as-narrator'));
    const merged = page.waitForRequest((req) => req.url().endsWith('/api/v1/authors/merge'));
    await page.getByTestId('author-variant-2').getByText('Author', { exact: true }).click();
    await expect(page.getByTestId('author-variant-2').getByText('Narrator')).toBeVisible();
    await page.getByRole('button', { name: 'Merge into "Author A"', exact: true }).click();
    await reclassified;
    expect((await merged).postDataJSON()).toEqual({ keep_id: 1, merge_ids: [3] });
  });

  test('the split button splits a composite author', async ({ page }) => {
    await openLane(page);
    await page.getByRole('button', { name: /split into 2 authors/i }).click();
    await expect(page.getByText(/Split "Author C Author D" into 2 authors/)).toBeVisible();
  });

  test('edits the canonical name inline', async ({ page }) => {
    await openLane(page);
    await page.getByRole('button', { name: 'Edit canonical name' }).first().click();
    const input = page.getByRole('textbox', { name: 'Canonical name' });
    await expect(input).toBeVisible();
    await expect(input).toHaveValue('Author A');
  });

  test('Validate shows what the providers returned', async ({ page }) => {
    await openLane(page);
    await page.getByTestId('author-group-1').getByRole('button', { name: 'Validate' }).click();
    await expect(page.getByTestId('author-group-1').getByTestId('author-validation-results')).toContainText('openlibrary');
  });

  test('? opens the lane\'s own shortcut help (the app-wide ? handler does not take it), and m does nothing behind it', async ({ page }) => {
    await openLane(page);
    await expect(page.getByRole('heading', { name: 'Author A', exact: true })).toBeVisible();
    await page.keyboard.press('?');
    await expect(page.getByTestId('authors-shortcut-help')).toBeVisible();
    const merges = await countRequests(page, '/api/v1/authors/merge', 'POST');
    await page.keyboard.press('m');
    expect(merges.n).toBe(0);
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('authors-shortcut-help')).toHaveCount(0);
  });

  test('shows the empty state when there are no duplicate groups', async ({ page }) => {
    await openLane(page, { groups: [] });
    await expect(page.getByTestId('authors-empty')).toContainText('No duplicate authors found');
  });

  test('a cold cache is "not scanned yet", not "no duplicates"', async ({ page }) => {
    await openLane(page, { groups: [], needsRefresh: true });
    await expect(page.getByTestId('authors-needs-scan')).toBeVisible();
    await expect(page.getByTestId('authors-empty')).toHaveCount(0);
  });
});

// ── Book Preview Popover ──

test.describe('Book Preview Popover', () => {
  test('clicking the book count chip opens a popover with the books', async ({ page }) => {
    await openLane(page);
    await page.getByText('12 credit(s)').click();
    await expect(page.getByText('Book One')).toBeVisible();
    await expect(page.getByText('Book Two')).toBeVisible();
    await expect(page.getByText('Book Three')).toBeVisible();
  });

  test('shows cover thumbnails when available', async ({ page }) => {
    await openLane(page);
    await page.getByText('12 credit(s)').click();
    await expect(page.locator('img[src*="covers/local"]')).toBeVisible();
  });

  test('clicking a book navigates to its detail page', async ({ page }) => {
    await openLane(page);
    await page.getByText('12 credit(s)').click();
    await page.getByText('Book One').click();
    await expect(page).toHaveURL(/\/library\/b1/);
  });

  test('closes when clicking outside', async ({ page }) => {
    await openLane(page);
    await page.getByText('12 credit(s)').click();
    await expect(page.getByText('Book One')).toBeVisible();
    await page.mouse.click(10, 10);
    await expect(page.getByText('Book One')).not.toBeVisible();
  });

  test('handles an author with no books', async ({ page }) => {
    await openLane(page, { books: [] });
    await page.getByText('12 credit(s)').click();
    await expect(page.getByText(/no books found/i)).toBeVisible();
  });

  test('a failed books read is an error with Retry, not "no books"', async ({ page }) => {
    await openLane(page);
    await page.route('**/api/v1/audiobooks?author_id=*', async (route) => {
      await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'books down' }) });
    });
    await page.getByText('12 credit(s)').click();
    await expect(page.getByText(/could not load/i)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
    await expect(page.getByText(/no books found/i)).toHaveCount(0);
  });
});

// ── Refresh & operations ──

test.describe('Dedup Refresh Operations', () => {
  test('the Refresh button starts the author scan and the bell shows it', async ({ page }) => {
    await openLane(page);
    await bellListsAfter(page, '/authors/duplicates/refresh', bellRow('dedup-refresh-1', 'dedup.author-scan', 'Author duplicate scan'));
    // Nothing listed before the click.
    await page.getByTestId('operations-bell').click();
    await expect(page.getByTestId('operations-empty')).toBeVisible();
    await page.keyboard.press('Escape');

    const scan = page.waitForRequest((req) => req.url().endsWith('/authors/duplicates/refresh') && req.method() === 'POST');
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await scan;
    await expect(page.getByText('Author scan started — watch the bell for progress.')).toBeVisible();
    await showBell(page);
    await expect(page.getByText('Author duplicate scan').first()).toBeVisible();
  });
});

// ── Pagination ──

test.describe('Dedup Pagination', () => {
  test('pages author groups 25 at a time', async ({ page }) => {
    const manyGroups: MockAuthorDedupGroup[] = Array.from({ length: 30 }, (_, i) => ({
      canonical: { id: i + 100, name: `Author Number ${i + 1}` },
      variants: [{ id: i + 200, name: `Auth Number ${i + 1}` }],
      book_count: i + 1,
    }));
    await openLane(page, { groups: manyGroups });

    await expect(page.getByRole('heading', { name: 'Author Number 1', exact: true })).toBeVisible();
    await expect(page.getByTestId(/^author-group-\d+$/)).toHaveCount(25);
    await expect(page.getByRole('heading', { name: 'Author Number 26', exact: true })).toHaveCount(0);

    await page.getByRole('button', { name: /next page/i }).click();
    await expect(page.getByRole('heading', { name: 'Author Number 26', exact: true })).toBeVisible();
    await expect(page.getByTestId(/^author-group-\d+$/)).toHaveCount(5);
  });
});

// ── Bulk actions ──

test.describe('Dedup Bulk Actions', () => {
  test('Merge All shows the count of groups a merge applies to', async ({ page }) => {
    await openLane(page);
    // Two name-variant groups; the composite and the two production companies
    // have no variants to merge and are not counted.
    await expect(page.getByRole('button', { name: 'Merge all 2 name-variant groups' })).toBeVisible();
  });

  test('Merge All asks first, and Cancel merges nothing', async ({ page }) => {
    await openLane(page);
    const merges = await countRequests(page, '/api/v1/authors/merge', 'POST');
    await page.getByRole('button', { name: /merge all/i }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'Confirm Merge All' })).toBeVisible();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toHaveCount(0);
    expect(merges.n).toBe(0);
  });

  test('confirming Merge All merges every mergeable group', async ({ page }) => {
    await openLane(page);
    const merges = await countRequests(page, '/api/v1/authors/merge', 'POST');
    await page.getByRole('button', { name: /merge all/i }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Confirm' }).click();
    await expect(page.getByText('Merged 2 of 2 name-variant group(s).')).toBeVisible();
    expect(merges.n).toBe(2);
  });

  test('a kind chip shows exactly the groups its count says', async ({ page }) => {
    await openLane(page);
    await expect(page.getByTestId('authors-kind-production')).toHaveText('Production companies (2)');
    await page.getByTestId('authors-kind-production').click();
    await expect(page.getByTestId(/^author-group-\d+$/)).toHaveCount(2);
  });
});

// ── Production company resolution ──

test.describe('Production Company Resolution', () => {
  test('Find Real Author appears only on production-company groups', async ({ page }) => {
    await openLane(page);
    await expect(page.getByRole('button', { name: /find real author/i })).toHaveCount(2);
    await expect(page.getByRole('button', { name: 'Merge into "Author A"', exact: true })).toBeVisible();
  });

  test('the production-company badge is distinguishable from other groups', async ({ page }) => {
    await openLane(page);
    await expect(page.getByText('Production Company')).toHaveCount(2);
    await expect(page.getByText('Author C', { exact: true })).toBeVisible();
  });
});

// ── Operation progress ──

test.describe('Dedup Operation Progress', () => {
  test('a merge reports through a toast and the bell lists the operation', async ({ page }) => {
    await openLane(page);
    await bellListsAfter(page, '/api/v1/authors/merge', bellRow('merge-op-1', 'dedup.author-merge', 'Merge authors'));
    await page.getByRole('button', { name: 'Merge into "Author A"', exact: true }).click();
    await expect(page.getByText('Merged author(s) into "Author A"')).toBeVisible();
    await showBell(page);
    await expect(page.getByText('Merge authors').first()).toBeVisible();
  });

  // A split is one synchronous request that answers with the new authors; it
  // starts no operation, so there is no bell entry to assert, only the toast.
  test('a split reports through a toast', async ({ page }) => {
    await openLane(page);
    await page.getByRole('button', { name: /split into 2 authors/i }).click();
    await expect(page.getByText(/Split ".*" into 2 authors/)).toBeVisible();
  });
});

// ── Error handling ──

test.describe('Dedup Error Handling', () => {
  test('a failed read is an error with Retry, not an empty list', async ({ page }) => {
    await setupMockApi(page, { books: generateTestBooks(3) });
    let fail = true;
    await page.route('**/api/v1/authors/duplicates', async (route) => {
      if (fail) {
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'Internal server error' }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: { groups: authorGroups, needs_refresh: false } }),
      });
    });
    await page.goto('/review?lane=authors');

    const alert = page.getByTestId('authors-error');
    await expect(alert).toContainText('Internal server error');
    await expect(page.getByTestId('authors-empty')).toHaveCount(0);

    fail = false;
    await alert.getByRole('button', { name: 'Retry' }).click();
    await expect(page.getByRole('heading', { name: 'Author A', exact: true })).toBeVisible();
    await expect(page.getByTestId('authors-error')).toHaveCount(0);
  });

  test('a refused merge is reported and the group stays', async ({ page }) => {
    await openLane(page);
    await page.route('**/api/v1/authors/merge', async (route) => {
      await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'Merge conflict' }) });
    });
    await page.getByRole('button', { name: 'Merge into "Author A"', exact: true }).click();
    await expect(page.getByText(/Merge conflict/)).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Author A', exact: true })).toBeVisible();
  });

  test('a failed resolve-production is reported', async ({ page }) => {
    await openLane(page);
    await page.route('**/api/v1/authors/*/resolve-production', async (route) => {
      await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'Resolution failed' }) });
    });
    await page.getByRole('button', { name: /find real author/i }).first().click();
    await expect(page.getByText(/Resolution failed/)).toBeVisible();
  });
});

// ── Series Dedup (the Series sub-view) ──

interface SeriesFixture {
  name: string;
  match_type?: string;
  suggested_name?: string;
  series: Array<{ id: number; name: string; author_id?: number; author_name?: string; books?: Array<{ id: string; title: string }> }>;
}

const seriesGroups: SeriesFixture[] = [
  {
    name: 'Series One',
    match_type: 'exact',
    suggested_name: 'Series One Preferred',
    series: [
      { id: 11, name: 'Series One', books: [{ id: 'sb1', title: 'Book One' }] },
      { id: 12, name: 'series one', author_id: 7, author_name: 'Author G', books: [{ id: 'sb1', title: 'Book One' }] },
    ],
  },
  {
    name: 'Series Two',
    match_type: 'exact',
    series: [
      { id: 21, name: 'Series Two' },
      { id: 22, name: 'series two' },
    ],
  },
  {
    name: 'Series Three, Book 2',
    match_type: 'subseries',
    series: [
      { id: 31, name: 'Series Three' },
      { id: 32, name: 'Series Three, Book 2' },
    ],
  },
];

const seriesPath = (id: string) => (id ? `/review?lane=authors&view=${id}` : '/review?lane=authors');

/** Mocks the series endpoints this lane calls. A route registered here beats setupMockApi's. */
async function openSeries(
  page: Page,
  opts: { groups?: SeriesFixture[]; needsRefresh?: boolean; total?: number; url?: string } = {}
) {
  const groups = opts.groups ?? seriesGroups;
  await setupMockApi(page, {
    books: generateTestBooks(5),
    authorDedup: { groups: authorGroups },
    operations: { active: [], history: [], logs: {} },
  });
  await page.route('**/api/v1/audiobooks?author_id=*', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], count: 0 }) });
  });
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: 'application/json',
    body: JSON.stringify({ data: body }),
  });
  await page.route('**/api/v1/series/duplicates', async (route) => {
    await route.fulfill(
      json({ groups: opts.needsRefresh ? [] : groups, count: groups.length, total_series: opts.total ?? 17, needs_refresh: Boolean(opts.needsRefresh) })
    );
  });
  await page.route('**/api/v1/series/merge', async (route) => {
    await route.fulfill(json({ id: 'series-merge-op', type: 'series-merge', status: 'queued' }, 202));
  });
  await page.route('**/api/v1/series/*/name', async (route) => {
    await route.fulfill(json({ id: 'series-rename-op', type: 'series-rename', status: 'queued' }, 202));
  });
  await page.route('**/api/v1/operations/series-rename-op/revert', async (route) => {
    await route.fulfill(json({ message: 'reverted', partial: false, restored: 1 }));
  });
  await page.goto(opts.url ?? seriesPath('series'));
  await expect(page.getByTestId('authors-subview-series')).toBeVisible();
}

test.describe('Series Dedup', () => {
  test('?view=series opens the Series sub-view, selected, and Authors still works from the control', async ({ page }) => {
    await openSeries(page);
    await expect(page.getByRole('tab', { name: 'Authors & series' })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByTestId('authors-subview-series')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.getByTestId('series-panel')).toBeVisible();
    await expect(page.getByTestId('series-group-s11-12')).toBeVisible();

    await page.getByTestId('authors-subview-authors').click();
    await expect(page.getByRole('heading', { name: 'Author A', exact: true })).toBeVisible();
    await expect(page.getByTestId('series-panel')).toHaveCount(0);
  });

  test('the sub-view control lists Authors, Series then AI scans, and the Series choice is not written to the URL', async ({ page }) => {
    await openSeries(page, { url: seriesPath('') });
    await expect(page.getByTestId('authors-panel')).toBeVisible();
    await expect(page.getByRole('group', { name: 'Authors & series sub-view' }).getByRole('button')).toHaveText(['Authors', 'Series', 'AI scans']);
    await page.getByTestId('authors-subview-series').click();
    await expect(page.getByTestId('series-panel')).toBeVisible();
    expect(new URL(page.url()).search).toBe('?lane=authors');
  });

  test('displays the groups: names, author chips, a kept series, and the counts', async ({ page }) => {
    await openSeries(page);
    const card = page.getByTestId('series-group-s11-12');
    await expect(card.getByRole('heading', { name: 'Series One' })).toBeVisible();
    await expect(card.getByText('2 series')).toBeVisible();
    await expect(card.getByText('Author G (id: 7)')).toBeVisible();
    await expect(card.getByTestId('series-row-12')).toContainText('Kept');
    await expect(card.getByTestId('series-row-11')).toContainText('Merged in');
    await expect(page.getByTestId('series-group-s31-32').getByText('sub-series')).toBeVisible();
    await expect(page.getByTestId('series-kind-all')).toHaveText('All groups (3)');
    await expect(page.getByTestId('series-kind-exact')).toHaveText('Same name (2)');
    await expect(page.getByTestId('series-total-link')).toHaveText('17 series in the library');
  });

  test('there is no Prune Series button in the lane', async ({ page }) => {
    await openSeries(page);
    await expect(page.getByTestId('series-group-s11-12')).toBeVisible();
    await expect(page.getByRole('button', { name: /prune/i })).toHaveCount(0);
  });

  test('empty state when there are no series duplicates', async ({ page }) => {
    await openSeries(page, { groups: [], total: 41 });
    await expect(page.getByTestId('series-empty')).toContainText('No duplicate series found');
    await expect(page.getByTestId('series-total-link')).toHaveText('41 series in the library');
  });

  test('a cold cache is "not scanned yet", not "no duplicates"', async ({ page }) => {
    await openSeries(page, { needsRefresh: true });
    await expect(page.getByTestId('series-needs-scan')).toBeVisible();
    await expect(page.getByTestId('series-empty')).toHaveCount(0);
  });

  test('the Same name chip opens exactly the groups Merge All counts', async ({ page }) => {
    await openSeries(page);
    await expect(page.getByRole('button', { name: 'Merge All (2)' })).toBeVisible();
    await page.getByTestId('series-kind-exact').click();
    await expect(page.getByTestId(/^series-group-s\d/)).toHaveCount(2);
    await page.getByTestId('series-kind-subseries').click();
    await expect(page.getByTestId(/^series-group-s\d/)).toHaveCount(1);
  });

  test('Merge keeps the author-linked series and merges the other into it', async ({ page }) => {
    await openSeries(page);
    const merged = page.waitForRequest((req) => req.url().endsWith('/api/v1/series/merge'));
    await page.getByRole('button', { name: 'Merge into "series one"' }).click();
    expect((await merged).postDataJSON()).toEqual({ keep_id: 12, merge_ids: [11] });
    await expect(page.getByText('Merged 1 series into "series one"')).toBeVisible();
    await expect(page.getByTestId('series-group-s11-12')).toHaveCount(0);
    // Edited in place: the group is gone without a second read of the list.
    await expect(page.getByTestId('series-group-s21-22')).toBeVisible();
  });

  test('a narrator flag is sent with the merge: the author is reclassified after it completes', async ({ page }) => {
    await openSeries(page);
    const order: string[] = [];
    page.on('request', (req) => {
      if (req.method() !== 'POST') return;
      if (req.url().includes('/authors/7/reclassify-as-narrator')) order.push('reclassify');
      if (req.url().endsWith('/api/v1/series/merge')) order.push('merge');
    });
    await page.getByTestId('series-row-12').getByText('Author', { exact: true }).click();
    await expect(page.getByTestId('series-row-12').getByText('Narrator')).toBeVisible();
    await page.getByRole('button', { name: 'Merge into "series one"' }).click();
    await expect(page.getByText('Merged 1 series into "series one"')).toBeVisible();
    // After the merge completed, never before: a merge that fails must not leave an
    // author reclassified.
    await expect.poll(() => order).toEqual(['merge', 'reclassify']);
  });

  test('leaving a series out keeps it out of the merge', async ({ page }) => {
    await openSeries(page, {
      groups: [
        {
          name: 'Series Four',
          match_type: 'exact',
          series: [
            { id: 41, name: 'Series Four' },
            { id: 42, name: 'series four' },
            { id: 43, name: 'SERIES FOUR' },
          ],
        },
      ],
    });
    await page.getByRole('checkbox', { name: 'Include series SERIES FOUR (ID 43) in the merge' }).uncheck();
    const merged = page.waitForRequest((req) => req.url().endsWith('/api/v1/series/merge'));
    await page.getByRole('button', { name: 'Merge into "Series Four"' }).click();
    expect((await merged).postDataJSON()).toEqual({ keep_id: 41, merge_ids: [42] });
  });

  test('renaming a series uses the undoable endpoint and offers an Undo', async ({ page }) => {
    await openSeries(page);
    const card = page.getByTestId('series-group-s11-12');
    await card.getByRole('button', { name: 'Edit series name Series One', exact: true }).click();
    const input = card.getByRole('textbox', { name: 'Series name' });
    await expect(input).toHaveValue('Series One');
    const renamed = page.waitForRequest((req) => /\/api\/v1\/series\/11\/name$/.test(req.url()) && req.method() === 'PUT');
    await input.fill('Series One Renamed');
    await input.press('Enter');
    expect((await renamed).postDataJSON()).toEqual({ name: 'Series One Renamed' });
    await expect(card.getByTestId('series-row-11')).toContainText('ID 11: "Series One Renamed"');

    const reverted = page.waitForRequest((req) => req.url().endsWith('/operations/series-rename-op/revert') && req.method() === 'POST');
    await card.getByRole('button', { name: 'Undo rename of Series One Renamed' }).click();
    await reverted;
    await expect(card.getByTestId('series-row-11')).toContainText('ID 11: "Series One"');
    await expect(card.getByTestId('series-row-11')).not.toContainText('Renamed');
    await expect(card.getByRole('button', { name: /Undo rename/ })).toHaveCount(0);
  });

  test('the suggested name starts an edit of the kept series', async ({ page }) => {
    await openSeries(page);
    await page.getByTestId('series-group-s11-12').getByText('Suggested: "Series One Preferred"').click();
    await expect(page.getByRole('textbox', { name: 'Series name' })).toHaveValue('Series One Preferred');
  });

  test('Validate asks the providers about the series name and shows what came back', async ({ page }) => {
    await openSeries(page);
    const validated = page.waitForRequest((req) => req.url().endsWith('/api/v1/dedup/validate'));
    await page.getByTestId('series-group-s11-12').getByRole('button', { name: 'Validate' }).click();
    expect((await validated).postDataJSON()).toEqual({ query: 'Series One', type: 'series' });
    await expect(page.getByTestId('series-group-s11-12').getByTestId('series-validation-results')).toContainText('openlibrary');
  });

  test('Merge All asks first, lists the groups, and Cancel merges nothing', async ({ page }) => {
    await openSeries(page);
    const merges = await countRequests(page, '/api/v1/series/merge', 'POST');
    await page.getByRole('button', { name: 'Merge All (2)' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'Confirm Merge All' })).toBeVisible();
    await expect(dialog.getByRole('listitem')).toHaveText(['Series One', 'Series Two']);
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toHaveCount(0);
    expect(merges.n).toBe(0);
  });

  test('confirming Merge All merges each same-name group and leaves the sub-series group', async ({ page }) => {
    await openSeries(page);
    const merges = await countRequests(page, '/api/v1/series/merge', 'POST');
    await page.getByRole('button', { name: 'Merge All (2)' }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Confirm' }).click();
    await expect(page.getByText('Merged 2 of 2 same-name group(s).')).toBeVisible();
    expect(merges.n).toBe(2);
    await expect(page.getByTestId(/^series-group-s\d/)).toHaveCount(1);
    await expect(page.getByTestId('series-group-s31-32')).toBeVisible();
  });

  test('Merge Selected (N) counts the ticked groups and merges only them', async ({ page }) => {
    await openSeries(page);
    await page.getByRole('checkbox', { name: 'Select group Series Two' }).check();
    const merged = page.waitForRequest((req) => req.url().endsWith('/api/v1/series/merge'));
    await page.getByRole('button', { name: 'Merge Selected (1)' }).click();
    expect((await merged).postDataJSON()).toEqual({ keep_id: 21, merge_ids: [22] });
    await expect(page.getByTestId('series-group-s21-22')).toHaveCount(0);
    await expect(page.getByTestId('series-group-s11-12')).toBeVisible();
  });

  test('a merge that left a series behind keeps the group with what remains, and says which', async ({ page }) => {
    await openSeries(page, {
      groups: [
        {
          name: 'Series Four',
          match_type: 'exact',
          series: [
            { id: 41, name: 'Series Four' },
            { id: 42, name: 'series four' },
            { id: 43, name: 'SERIES FOUR' },
          ],
        },
      ],
    });
    await page.route('**/api/v1/operations/v2/series-merge-op', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            operation: {
              id: 'series-merge-op',
              def_id: 'dedup.series-merge',
              status: 'failed',
              error_message: 'series merge left series ids [43]: series merge merged 1 of 2 series with 1 error(s)',
            },
            logs: [],
          },
        }),
      });
    });
    await page.getByRole('button', { name: 'Merge into "Series Four"' }).click();
    await expect(page.getByText(/1 series was not merged \(ids 43\)/)).toBeVisible();
    // 42 went; 41 (kept) and 43 remain: still a duplicate pair.
    const card = page.getByTestId('series-group-s41-42-43');
    await expect(card.getByTestId('series-row-41')).toBeVisible();
    await expect(card.getByTestId('series-row-43')).toBeVisible();
    await expect(card.getByTestId('series-row-42')).toHaveCount(0);
  });

  test('a refused merge is reported and the group stays', async ({ page }) => {
    await openSeries(page);
    await page.route('**/api/v1/series/merge', async (route) => {
      await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'Merge conflict' }) });
    });
    await page.getByRole('button', { name: 'Merge into "series one"' }).click();
    await expect(page.getByText(/Merge conflict/)).toBeVisible();
    await expect(page.getByTestId('series-group-s11-12')).toBeVisible();
  });

  test('a failed read is an error with Retry, not an empty list', async ({ page }) => {
    await setupMockApi(page, { books: generateTestBooks(3) });
    let fail = true;
    await page.route('**/api/v1/series/duplicates', async (route) => {
      if (fail) {
        await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'Internal server error' }) });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: { groups: [], total_series: 4, needs_refresh: false } }),
      });
    });
    await page.goto(seriesPath('series'));
    const alert = page.getByTestId('series-error');
    await expect(alert).toContainText('Internal server error');
    await expect(page.getByTestId('series-empty')).toHaveCount(0);
    fail = false;
    await alert.getByRole('button', { name: 'Retry' }).click();
    await expect(page.getByTestId('series-empty')).toBeVisible();
    await expect(page.getByTestId('series-error')).toHaveCount(0);
  });

  test('switching to Authors and back reads each list once per visit, never the other one', async ({ page }) => {
    const seriesReads = { n: 0 };
    const authorReads = { n: 0 };
    page.on('request', (req) => {
      if (req.method() !== 'GET') return;
      if (req.url().endsWith('/api/v1/series/duplicates')) seriesReads.n++;
      if (req.url().endsWith('/api/v1/authors/duplicates')) authorReads.n++;
    });
    await openSeries(page);
    await expect(page.getByTestId('series-group-s11-12')).toBeVisible();
    expect(seriesReads.n).toBe(1);
    expect(authorReads.n).toBe(0);

    await page.getByTestId('authors-subview-authors').click();
    await expect(page.getByRole('heading', { name: 'Author A', exact: true })).toBeVisible();
    expect(authorReads.n).toBe(1);
    expect(seriesReads.n).toBe(1);

    await page.getByTestId('authors-subview-series').click();
    await expect(page.getByTestId('series-group-s11-12')).toBeVisible();
    expect(seriesReads.n).toBe(2);
    expect(authorReads.n).toBe(1);
  });

  test('keyboard: m does nothing while the "Groups per page" menu is open', async ({ page }) => {
    await openSeries(page);
    await expect(page.getByTestId('series-group-s11-12')).toBeVisible();
    const merges = await countRequests(page, '/api/v1/series/merge', 'POST');
    await page.getByRole('combobox', { name: /groups per page/i }).click();
    await expect(page.getByRole('listbox')).toBeVisible();
    await page.keyboard.press('m');
    expect(merges.n).toBe(0);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('listbox')).toHaveCount(0);
  });

  test('keyboard: m merges the focused series group, and not while the help is open', async ({ page }) => {
    await openSeries(page);
    await expect(page.getByTestId('series-group-s11-12')).toHaveAttribute('data-focused', 'true');
    await page.keyboard.press('?');
    await expect(page.getByTestId('series-shortcut-help')).toBeVisible();
    const merges = await countRequests(page, '/api/v1/series/merge', 'POST');
    await page.keyboard.press('m');
    expect(merges.n).toBe(0);
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('series-shortcut-help')).toHaveCount(0);
    const merged = page.waitForRequest((req) => req.url().endsWith('/api/v1/series/merge'));
    await page.keyboard.press('m');
    expect((await merged).postDataJSON()).toEqual({ keep_id: 12, merge_ids: [11] });
  });
});

// ── AI scans ──

interface AIScanFixture {
  id: number;
  status: string;
  mode: 'realtime' | 'batch';
  superseded_by?: number;
  author_count: number;
  created_at: string;
  models: { groups: string; full: string };
}

const aiScan = (id: number, over: Partial<AIScanFixture> = {}): AIScanFixture => ({
  id,
  status: 'complete',
  mode: 'realtime',
  author_count: 3,
  created_at: '2026-10-01T09:00:00Z',
  models: { groups: 'model-g', full: 'model-f' },
  ...over,
});

/** A merge row keeping author id*10 and folding in id*10+1. */
const aiResult = (id: number, agreement: string, canonical: string, applied = false, action = 'merge') => ({
  id,
  scan_id: 5,
  agreement,
  applied,
  suggestion: {
    action,
    canonical_name: canonical,
    reason: 'same person',
    confidence: 'high',
    author_ids: [id * 10, id * 10 + 1],
    ...(action === 'merge' ? { keep_id: id * 10 } : {}),
    source: 'full_scan',
  },
});

const aiResults = [
  aiResult(1, 'agreed', 'Author A'),
  aiResult(2, 'agreed', 'Author B', true),
  aiResult(3, 'groups_only', 'Author C'),
  aiResult(4, 'disagreed', 'Author D'),
  // A rename: read-only here, whatever its agreement.
  aiResult(5, 'disagreed', 'Author E', false, 'rename'),
];

const AI_AUTHOR_NAMES = { 10: 'Author A', 11: 'A. Author', 40: 'Author D', 41: 'D. Author' };

/** Mocks the AI scan endpoints this sub-view calls. A route registered here beats setupMockApi's. */
async function openAI(
  page: Page,
  opts: { scans?: AIScanFixture[]; results?: unknown[]; url?: string; historyFails?: () => boolean } = {}
) {
  const scans = opts.scans ?? [aiScan(6, { status: 'superseded', superseded_by: 12, mode: 'batch' }), aiScan(5)];
  await setupMockApi(page, {
    books: generateTestBooks(5),
    authorDedup: { groups: authorGroups },
    operations: { active: [], history: [], logs: {} },
  });
  const json = (body: unknown, status = 202) => ({ status, contentType: 'application/json', body: JSON.stringify({ data: body }) });
  await page.route('**/api/v1/ai/scans', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill(json(aiScan(7, { status: 'pending' }), 202));
      return;
    }
    if (opts.historyFails?.()) {
      await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'Internal server error' }) });
      return;
    }
    await route.fulfill(json({ scans }, 200));
  });
  await page.route('**/api/v1/ai/scans/*/results', async (route) => {
    await route.fulfill(json({ results: opts.results ?? aiResults, author_names: AI_AUTHOR_NAMES }, 200));
  });
  // Apply queues an operation (202); the page follows it to the end.
  await page.route('**/api/v1/ai/scans/*/apply', async (route) => {
    await route.fulfill(json({ id: 'ai-apply-op', type: 'ai.author-scan-apply', status: 'queued', scan_id: 5, results: 2 }, 202));
  });
  await page.route('**/api/v1/operations/v2/ai-apply-op', async (route) => {
    await route.fulfill(
      json({ operation: { id: 'ai-apply-op', def_id: 'ai.author-scan-apply', status: 'completed', error_message: null }, logs: [] }, 200)
    );
  });
  await page.route('**/api/v1/ai/scans/*/cancel', async (route) => {
    await route.fulfill(json({ status: 'canceled' }, 200));
  });
  await page.goto(opts.url ?? '/review?lane=authors&view=ai');
  await expect(page.getByTestId('ai-scans-panel')).toBeVisible();
}

test.describe('Dedup AI Review', () => {
  test('?view=ai opens AI scans, selected; the control reads Authors, Series, AI scans; there is no Books tab', async ({ page }) => {
    await openAI(page);
    await expect(page.getByRole('tab', { name: 'Authors & series' })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByTestId('authors-subview-ai')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.getByRole('group', { name: 'Authors & series sub-view' }).getByRole('button')).toHaveText(['Authors', 'Series', 'AI scans']);
    await expect(page.getByText('Books', { exact: true })).toHaveCount(0);
    expect(new URL(page.url()).search).toBe('?lane=authors&view=ai');
  });

  test('the history lists every scan, the nightly superseded one marked, and it is not a drawer', async ({ page }) => {
    await openAI(page);
    const list = page.getByTestId('ai-history-list');
    await expect(list.getByTestId(/^ai-scan-\d+$/)).toHaveCount(2);
    await expect(list.getByTestId('ai-scan-superseded-6')).toHaveText('superseded');
    await expect(list.getByTestId('ai-scan-replaced-by-6')).toHaveText('Replaced by scan #12 before anything was applied from it');
    await expect(list.getByTestId('ai-scan-5')).toContainText('realtime');
    await expect(page.getByRole('dialog')).toHaveCount(0);
  });

  test('agreement chips show counts, and clicking one shows exactly that many rows', async ({ page }) => {
    await openAI(page);
    await page.getByTestId('ai-scan-5').click();
    await expect(page.getByTestId('ai-result-1')).toBeVisible();
    await expect(page.getByTestId('ai-agreement-all')).toHaveText('All (5)');
    await expect(page.getByTestId('ai-agreement-agreed')).toHaveText('Agreed (2)');
    await expect(page.getByTestId('ai-agreement-groups_only')).toHaveText('Groups Only (1)');
    await expect(page.getByTestId('ai-agreement-full_only')).toHaveText('Full Only (0)');
    await expect(page.getByTestId('ai-agreement-disagreed')).toHaveText('Disagreed (2)');

    await page.getByTestId('ai-agreement-disagreed').click();
    await expect(page.getByTestId(/^ai-result-\d+$/)).toHaveCount(2);
    await page.getByTestId('ai-agreement-full_only').click();
    await expect(page.getByTestId('ai-results-filter-empty')).toBeVisible();
    await expect(page.getByTestId(/^ai-result-\d+$/)).toHaveCount(0);
  });

  test('a merge row says which author it keeps and which it folds in, by name', async ({ page }) => {
    await openAI(page);
    await page.getByTestId('ai-scan-5').click();
    await expect(page.getByTestId('ai-result-plan-1')).toHaveText('keep Author A, merge A. Author');
    // Author 30 was merged away since the scan: named by id, not blank.
    await expect(page.getByTestId('ai-result-plan-3')).toContainText('author #30 (no longer exists)');
  });

  test('a rename row is read-only, with a link to the Authors view; a disagreed merge row stays selectable', async ({ page }) => {
    await openAI(page);
    await page.getByTestId('ai-scan-5').click();
    await expect(page.getByRole('checkbox', { name: 'Select result 5' })).toBeDisabled();
    await expect(page.getByTestId('ai-result-readonly-5')).toContainText('Read-only here');
    await expect(page.getByTestId('ai-result-authors-link-5')).toHaveAttribute('href', '/review?lane=authors&author=50');
    await expect(page.getByRole('checkbox', { name: 'Select result 4' })).toBeEnabled();
  });

  test('selecting results and clicking Apply selected (N) sends exactly those ids, then follows the operation and re-reads', async ({ page }) => {
    await openAI(page);
    await page.getByTestId('ai-scan-5').click();
    await expect(page.getByTestId('ai-result-1')).toBeVisible();
    // The applied result (2) cannot be ticked.
    await expect(page.getByRole('checkbox', { name: 'Select result 2' })).toBeDisabled();
    await page.getByRole('checkbox', { name: 'Select result 1' }).check();
    await page.getByRole('checkbox', { name: 'Select result 4' }).check();
    const applied = page.waitForRequest((req) => /\/ai\/scans\/5\/apply$/.test(req.url()) && req.method() === 'POST');
    const reread = page.waitForRequest((req) => /\/ai\/scans\/5\/results$/.test(req.url()), { timeout: 15_000 });
    await page.getByRole('button', { name: 'Apply selected (2)' }).click();
    expect((await applied).postDataJSON()).toEqual({ result_ids: [1, 4] });
    // Not the old "Applied N of M": the merges ran in an operation.
    await expect(page.getByText('AI scan apply started — watch the bell for progress.')).toBeVisible();
    await expect(page.getByTestId('ai-scans-notice')).toContainText('Merged the authors of 2 selected results.');
    await reread;
  });

  test('a superseded scan\'s results are read-only, and an apply the server refuses shows its message', async ({ page }) => {
    await openAI(page);
    // Opened already superseded: readable, nothing to tick.
    await page.getByTestId('ai-scan-6').click();
    await expect(page.getByTestId('ai-scan-superseded')).toContainText('superseded by scan #12');
    await expect(page.getByRole('checkbox', { name: 'Select result 1' })).toBeDisabled();

    // Opened while complete, superseded before Apply is clicked.
    await page.getByTestId('ai-scan-5').click();
    await page.getByRole('checkbox', { name: 'Select result 1' }).check();
    const message = 'scan 5 was superseded by scan 12; apply from scan 12 instead';
    await page.route('**/api/v1/ai/scans/5/apply', async (route) => {
      await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: message, code: 'CONFLICT', status: 409 }) });
    });
    await page.getByRole('button', { name: 'Apply selected (1)' }).click();
    await expect(page.getByTestId('ai-scans-notice')).toContainText(message);
    await expect(page.getByRole('button', { name: /apply selected/i })).toHaveCount(0);
  });

  test('a failed history read is an error with Retry, not an empty list', async ({ page }) => {
    let fail = true;
    await openAI(page, { historyFails: () => fail });
    const alert = page.getByTestId('ai-history-error');
    await expect(alert).toContainText('Internal server error');
    await expect(page.getByTestId('ai-history-empty')).toHaveCount(0);
    fail = false;
    await alert.getByRole('button', { name: 'Retry' }).click();
    await expect(page.getByTestId('ai-history-list')).toBeVisible();
    await expect(page.getByTestId('ai-history-error')).toHaveCount(0);
  });

  test('Start AI author scan in the lane menu starts the scan, reports it, and the bell shows it', async ({ page }) => {
    await openAI(page);
    let started = false;
    page.on('request', (req) => {
      if (req.method() === 'POST' && req.url().endsWith('/api/v1/ai/scans')) started = true;
    });
    await page.route('**/api/v1/operations/timeline*', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: { operations: started ? [bellRow('ai-scan-op-1', 'ai.author-scan', 'AI author scan')] : [] } }),
      });
    });

    const posted = page.waitForRequest((req) => req.url().endsWith('/api/v1/ai/scans') && req.method() === 'POST');
    await page.getByTestId('command-menu-authors').click();
    await page.getByTestId('command-ai-author-scan-start').click();
    expect((await posted).postDataJSON()).toEqual({ mode: 'realtime' });
    await expect(page.getByText('AI author scan started — watch the bell for progress.')).toBeVisible();

    // The page did not freeze, and nothing polls: reload makes the bell read the timeline.
    await page.reload();
    await expect(page.getByTestId('ai-scans-panel')).toBeVisible();
    await page.getByTestId('operations-bell').click();
    await expect(page.getByText('AI author scan').first()).toBeVisible();
  });

  test('Start runs in Batch mode when the switch is on, and is unavailable outside AI scans', async ({ page }) => {
    await openAI(page);
    const mode = page.getByRole('group', { name: 'Scan mode' });
    await expect(mode.getByRole('button', { name: 'Realtime (faster, more expensive)' })).toHaveAttribute('aria-pressed', 'true');
    await mode.getByRole('button', { name: 'Batch (cheaper, hours)' }).click();
    await expect(mode.getByRole('button', { name: 'Batch (cheaper, hours)' })).toHaveAttribute('aria-pressed', 'true');
    const posted = page.waitForRequest((req) => req.url().endsWith('/api/v1/ai/scans') && req.method() === 'POST');
    await page.getByTestId('command-menu-authors').click();
    await page.getByTestId('command-ai-author-scan-start').click();
    expect((await posted).postDataJSON()).toEqual({ mode: 'batch' });

    await page.getByTestId('authors-subview-authors').click();
    await page.getByTestId('command-menu-authors').click();
    await expect(page.getByTestId('command-ai-author-scan-start')).toHaveAttribute('aria-disabled', 'true');
  });

  test('Cancel scan in the lane menu cancels the running scan', async ({ page }) => {
    await openAI(page, { scans: [aiScan(7, { status: 'scanning' }), aiScan(5)] });
    await expect(page.getByTestId('ai-scan-7')).toContainText('scanning');
    await page.getByTestId('command-menu-authors').click();
    // A scan is running, so Start waits and Cancel is live.
    await expect(page.getByTestId('command-ai-author-scan-start')).toHaveAttribute('aria-disabled', 'true');
    const canceled = page.waitForRequest((req) => /\/ai\/scans\/7\/cancel$/.test(req.url()) && req.method() === 'POST');
    await page.getByTestId('command-ai-author-scan-cancel').click();
    await canceled;
    await expect(page.getByText('Scan #7 canceled.')).toBeVisible();
  });
});
