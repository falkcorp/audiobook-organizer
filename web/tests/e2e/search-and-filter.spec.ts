// file: web/tests/e2e/search-and-filter.spec.ts
// version: 1.6.0
// guid: c3d4e5f6-a7b8-9012-cdef-a3b4c5d6e7f8
// last-edited: 2026-08-09

import { test, expect } from '@playwright/test';
import { setupLibraryWithBooks, generateTestBooks, waitForMenuClosed } from './utils/test-helpers';

test.describe('Search and Filter Functionality', () => {
  // Setup handled by setupLibraryWithBooks() which calls setupMockApi()
  // (includes skipWelcomeWizard + mockEventSource + setupMockApiRoutes)

  test('searches books by exact title match', async ({ page }) => {
    // GIVEN: Library has book titled "The Way of Kings"
    const books = [
      {
        ...generateTestBooks(1)[0],
        id: 'book-1',
        title: 'The Way of Kings',
        author_name: 'Brandon Sanderson',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-2',
        title: 'The Hobbit',
        author_name: 'J.R.R. Tolkien',
      },
    ];
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // WHEN: User types "The Way of Kings" in search box
    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('The Way of Kings');

    // THEN: Shows only books matching that title
    await expect(
      page.getByRole('heading', { name: 'The Way of Kings', exact: true })
    ).toBeVisible();
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).not.toBeVisible();
  });

  test('searches books by partial title match', async ({ page }) => {
    // GIVEN: Library has books with "Foundation" in title
    const books = [
      {
        ...generateTestBooks(1)[0],
        id: 'book-1',
        title: 'Foundation',
        author_name: 'Isaac Asimov',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-2',
        title: 'Foundation and Empire',
        author_name: 'Isaac Asimov',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-3',
        title: 'The Hobbit',
        author_name: 'J.R.R. Tolkien',
      },
    ];
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // WHEN: User types "Found" in search box
    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('Found');

    // THEN: Shows all books with "Found" in title
    await expect(page.getByRole('heading', { name: 'Foundation', exact: true })).toBeVisible();
    await expect(
      page.getByRole('heading', { name: 'Foundation and Empire', exact: true })
    ).toBeVisible();
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).not.toBeVisible();
  });

  test('searches books by author name', async ({ page }) => {
    // GIVEN: Library has books by multiple authors
    const books = [
      {
        ...generateTestBooks(1)[0],
        id: 'book-1',
        title: 'The Way of Kings',
        author_name: 'Brandon Sanderson',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-2',
        title: 'The Hobbit',
        author_name: 'J.R.R. Tolkien',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-3',
        title: 'Words of Radiance',
        author_name: 'Brandon Sanderson',
      },
    ];
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // WHEN: User types "Sanderson" in search
    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('Sanderson');

    // THEN: Shows all books by authors matching "Sanderson"
    await expect(
      page.getByRole('heading', { name: 'The Way of Kings', exact: true })
    ).toBeVisible();
    await expect(
      page.getByRole('heading', { name: 'Words of Radiance', exact: true })
    ).toBeVisible();
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).not.toBeVisible();
  });

  test('searches books by series name', async ({ page }) => {
    // GIVEN: Library has "Stormlight Archive" series
    const books = [
      {
        ...generateTestBooks(1)[0],
        id: 'book-1',
        title: 'The Way of Kings',
        author_name: 'Brandon Sanderson',
        series_name: 'The Stormlight Archive',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-2',
        title: 'Words of Radiance',
        author_name: 'Brandon Sanderson',
        series_name: 'The Stormlight Archive',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-3',
        title: 'The Hobbit',
        author_name: 'J.R.R. Tolkien',
        series_name: null,
      },
    ];
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // WHEN: User types "Stormlight" in search box
    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('Stormlight');

    // THEN: Shows all books in series matching "Stormlight"
    await expect(
      page.getByRole('heading', { name: 'The Way of Kings', exact: true })
    ).toBeVisible();
    await expect(
      page.getByRole('heading', { name: 'Words of Radiance', exact: true })
    ).toBeVisible();
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).not.toBeVisible();
  });

  test('search is case-insensitive', async ({ page }) => {
    // GIVEN: Library has "The Hobbit"
    const books = [
      {
        ...generateTestBooks(1)[0],
        id: 'book-1',
        title: 'The Hobbit',
        author_name: 'J.R.R. Tolkien',
      },
    ];
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // WHEN: User types "the hobbit" (lowercase) in search
    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('the hobbit');

    // THEN: Shows "The Hobbit" book
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).toBeVisible();
  });

  test('shows no results message when search matches nothing', async ({ page }) => {
    // GIVEN: Library loaded
    const books = generateTestBooks(5);
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // WHEN: User types "zzznonexistent" in search
    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('zzznonexistent');

    // THEN: Shows "No audiobooks found" message
    await expect(page.getByText(/no results for/i)).toBeVisible();
  });

  test('clears search with clear button', async ({ page }) => {
    // GIVEN: User has typed "Foundation" in search
    const books = [
      {
        ...generateTestBooks(1)[0],
        id: 'book-1',
        title: 'Foundation',
        author_name: 'Isaac Asimov',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-2',
        title: 'The Hobbit',
        author_name: 'J.R.R. Tolkien',
      },
    ];
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('Foundation');

    // AND: Results are filtered
    await expect(page.getByRole('heading', { name: 'Foundation', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).not.toBeVisible();

    // WHEN: User clicks "X" (clear search) button
    await page
      .locator('button', {
        has: page.locator('svg[data-testid="ClearIcon"]'),
      })
      .click();

    // THEN: Search input is cleared
    await expect(searchInput).toHaveValue('');

    // AND: All books are shown again
    await expect(page.getByRole('heading', { name: 'Foundation', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).toBeVisible();
  });

  test('clears search with backspace to empty', async ({ page }) => {
    // GIVEN: User has typed "Foundation" in search
    const books = [
      {
        ...generateTestBooks(1)[0],
        id: 'book-1',
        title: 'Foundation',
        author_name: 'Isaac Asimov',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-2',
        title: 'The Hobbit',
        author_name: 'J.R.R. Tolkien',
      },
    ];
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('Foundation');

    // Verify filtered
    await expect(page.getByRole('heading', { name: 'Foundation', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).not.toBeVisible();

    // WHEN: User backspaces to empty string
    await searchInput.clear();

    // THEN: All books are shown again
    await expect(page.getByRole('heading', { name: 'Foundation', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).toBeVisible();
  });

  // KNOWN BUG. api.searchBooksPage (services/api.ts:1023-1037) sends only
  // search, limit, offset, is_primary_version and optionally show_quarantined.
  // It takes no library_state, no `filters`, no tags and no sort_by — and
  // useLibraryQuery routes through it for ANY non-empty search
  // (useLibraryQuery.ts:192-193). So the moment a user types in the search box,
  // every active filter and the sort order are silently dropped, while the
  // Filters chip keeps showing its count. Same family as the Deleted-filter
  // cache bug fixed in #2230: a filter that silently does nothing.
  // See todo.d/20260809-search-drops-filters-and-debounce.md.
  test.fixme('search works with other filters combined', async ({ page }) => {
    // GIVEN: Library has organized and import books by same author
    const books = [
      {
        ...generateTestBooks(1)[0],
        id: 'book-1',
        title: 'Organized Book',
        author_name: 'Brandon Sanderson',
        library_state: 'organized',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-2',
        title: 'Import Book',
        author_name: 'Brandon Sanderson',
        library_state: 'import',
      },
    ];
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // WHEN: User selects "Organized" state filter
    await page.getByRole('button', { name: /filters/i }).click();
    await page.getByRole('combobox', { name: 'Library State' }).click();
    await page.getByRole('option', { name: 'Organized' }).click();
    await waitForMenuClosed(page);
    await page.keyboard.press('Escape');

    // AND: User types "Sanderson" in search
    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('Sanderson');

    // THEN: Only organized books by Sanderson are shown
    await expect(page.getByRole('heading', { name: 'Organized Book', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Import Book', exact: true })).not.toBeVisible();
  });

  test('search persists across page navigation', async ({ page }) => {
    // GIVEN: User has searched for "Foundation"
    const books = [
      {
        ...generateTestBooks(1)[0],
        id: 'book-1',
        title: 'Foundation',
        author_name: 'Isaac Asimov',
      },
      {
        ...generateTestBooks(1)[0],
        id: 'book-2',
        title: 'The Hobbit',
        author_name: 'J.R.R. Tolkien',
      },
    ];
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('Foundation');

    // WHEN: User clicks a book to view details
    await page.getByRole('heading', { name: 'Foundation', exact: true }).click();
    await page.waitForLoadState('networkidle');

    // AND: User clicks browser back button
    await page.goBack();
    await page.waitForLoadState('networkidle');

    // THEN: Search term remains
    await expect(searchInput).toHaveValue('Foundation');
    await expect(page.getByRole('heading', { name: 'Foundation', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'The Hobbit', exact: true })).not.toBeVisible();
  });

  test('search updates URL with query parameter', async ({ page }) => {
    // GIVEN: Library page loaded
    const books = generateTestBooks(2);
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // WHEN: User types "Hobbit" in search
    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.fill('Hobbit');

    // THEN: URL updates to ?search=Hobbit
    await page.waitForFunction(() => window.location.search.includes('search=Hobbit'));
  });

  // KNOWN BUG. Measured 2026-08-09: typing the 10 characters of "Foundation"
  // fires 10 requests — exactly one per keystroke. There is no debounce at all,
  // despite this test's name. On a large library that is ten full-text queries
  // where one would do. Documented with the filter-dropping bug above in
  // todo.d/20260809-search-drops-filters-and-debounce.md.
  test.fixme('search debounces input to avoid excessive requests', async ({ page }) => {
    // GIVEN: Library page loaded
    const books = generateTestBooks(5);
    await setupLibraryWithBooks(page, books);

    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // Track search/audiobooks API requests after initial load
    const searchRequests: string[] = [];
    page.on('request', (req) => {
      const url = req.url();
      if (url.includes('/api/v1/audiobooks') && url.includes('search')) {
        searchRequests.push(url);
      }
    });

    const searchInput = page.getByPlaceholder(/search audiobooks/i);
    await searchInput.click();
    await page.keyboard.type('Foundation', { delay: 10 });

    // Wait for debounce to fire
    await page.waitForTimeout(1000);

    // THEN: Search should have fired a small number of times (debounced), not once per keystroke
    expect(searchRequests.length).toBeLessThanOrEqual(3);
  });
});
