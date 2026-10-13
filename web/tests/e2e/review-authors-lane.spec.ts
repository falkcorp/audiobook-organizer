// file: web/tests/e2e/review-authors-lane.spec.ts
// version: 1.0.0
// guid: 3f9a1d68-2c74-4b05-8e13-a7d06c5b9e42
// last-edited: 2026-10-13

// End-to-end coverage for the Authors & series lane of /review (Authors sub-view).
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
    expect(await page.getByRole('button', { name: /find real author/i }).count()).toBe(2);
    await expect(page.getByRole('button', { name: 'Merge into "Author A"', exact: true })).toBeVisible();
  });

  test('the production-company badge is distinguishable from other groups', async ({ page }) => {
    await openLane(page);
    expect(await page.getByText('Production Company').count()).toBe(2);
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
