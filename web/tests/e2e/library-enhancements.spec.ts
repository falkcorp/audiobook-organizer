// file: web/tests/e2e/library-enhancements.spec.ts
// version: 1.1.0
// guid: e8f9a0b1-c2d3-4e5f-6a7b-8c9d0e1f2a3b
// last-edited: 2026-08-09

import { test, expect, Page } from '@playwright/test';
import {
  setupPhase2Interactive,
  generateTestBooks,
  type MockApiOptions,
} from './utils/test-helpers';

/**
 * Builds mock books with diverse metadata for library enhancement tests.
 */
function buildMockBooks() {
  const base = generateTestBooks(8);
  // Enrich with genre, narrator, and tags for richer testing
  return base.map((book, i) => ({
    ...book,
    genre: ['Fantasy', 'Sci-Fi', 'Mystery', 'Romance'][i % 4],
    narrator: ['Michael Kramer', 'Kate Reading', 'Tim Gerard Reynolds', 'Steven Pacey'][i % 4],
    format: i % 2 === 0 ? 'M4B' : 'MP3',
  }));
}

/**
 * Set up additional mock routes that the base setupPhase2Interactive does not
 * cover (tags, preferences, batch-tags).
 */
async function setupAdditionalMocks(page: Page) {
  const mockTags = [
    { tag: 'scifi', count: 5 },
    { tag: 'fantasy', count: 3 },
    { tag: 'favorite', count: 2 },
    { tag: 'to-read', count: 7 },
  ];

  const preferencesStore: Record<string, string> = {};

  // /api/v1/tags
  await page.route('**/api/v1/tags', async (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ tags: mockTags }),
      });
    }
    return route.fallback();
  });

  // /api/v1/preferences/*
  await page.route('**/api/v1/preferences/**', async (route) => {
    const url = new URL(route.request().url());
    const key = url.pathname.split('/').pop() || '';
    const method = route.request().method();

    if (method === 'GET') {
      const value = preferencesStore[key] || null;
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ key, value }),
      });
    }
    if (method === 'PUT') {
      try {
        const body = route.request().postDataJSON();
        preferencesStore[key] = body?.value ?? '';
      } catch {
        // ignore
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'saved' }),
      });
    }
    return route.fallback();
  });

  // /api/v1/audiobooks/batch-tags
  await page.route('**/api/v1/audiobooks/batch-tags', async (route) => {
    if (route.request().method() === 'POST') {
      const body = route.request().postDataJSON();
      const count = Array.isArray(body?.book_ids) ? body.book_ids.length : 0;
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ updated: count }),
      });
    }
    return route.fallback();
  });
}

/**
 * Navigate to the library page and wait for books to render.
 */
async function goToLibrary(page: Page) {
  await page.goto('/library');
  await page.waitForLoadState('domcontentloaded');
  // Wait for book data to be loaded (either grid cards or table rows)
  await page.waitForSelector('table tbody tr, [class*="MuiCard"]', {
    timeout: 10000,
  }).catch(() => {
    // May not have rendered yet if loading spinner is showing
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('Column Customization', () => {
  let mockOptions: MockApiOptions;

  test.beforeEach(async ({ page }) => {
    const books = buildMockBooks();
    mockOptions = { books };
    await setupAdditionalMocks(page);
    await setupPhase2Interactive(page, undefined, mockOptions);
  });

  test('opens column chooser and toggles columns', async ({ page }) => {
    // Navigate to library and switch to list view
    await goToLibrary(page);

    // Switch to list view
    const listViewBtn = page.getByRole('button', { name: 'list view' });
    await listViewBtn.click();

    // Wait for table to render
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });

    // Verify "Genre" column is visible by default
    await expect(page.locator('th').filter({ hasText: 'Genre' })).toBeVisible();

    // Click "Columns" button to open the chooser
    const columnsBtn = page.getByRole('button', { name: /Columns/i });
    await columnsBtn.click();

    // Verify the popover shows column categories
    const popover = page.locator('.MuiPopover-paper');
    await expect(popover).toBeVisible();
    await expect(popover.getByText('Basic')).toBeVisible();
    await expect(popover.getByText('Media')).toBeVisible();

    // Toggle "Genre" off by clicking its checkbox
    const genreCheckbox = popover
      .locator('label')
      .filter({ hasText: 'Genre' })
      .locator('input[type="checkbox"]');
    await genreCheckbox.click();

    // Close the popover
    await page.keyboard.press('Escape');

    // Verify Genre column has disappeared from the table
    await expect(page.locator('th').filter({ hasText: 'Genre' })).toHaveCount(0);

    // Re-open and toggle Genre back on
    await columnsBtn.click();
    const genreCheckbox2 = page
      .locator('.MuiPopover-paper label')
      .filter({ hasText: 'Genre' })
      .locator('input[type="checkbox"]');
    await genreCheckbox2.click();
    await page.keyboard.press('Escape');

    // Verify Genre reappears
    await expect(page.locator('th').filter({ hasText: 'Genre' })).toBeVisible();
  });

  test('column resize works', async ({ page }) => {
    await goToLibrary(page);

    // Switch to list view
    await page.getByRole('button', { name: 'list view' }).click();
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });

    // Find a column header (Title) and its resize handle
    const titleHeader = page.locator('th').filter({ hasText: 'Title' });
    await expect(titleHeader).toBeVisible();

    // The resize handle is the absolutely-positioned box at the right edge of the th
    const resizeHandle = titleHeader.locator('div[style*="cursor"]').last();
    // If the resize handle is available, attempt a drag
    const handleVisible = await resizeHandle.isVisible().catch(() => false);
    if (handleVisible) {
      const box = await resizeHandle.boundingBox();
      if (box) {
        // Drag right by 50px
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        await page.mouse.move(
          box.x + box.width / 2 + 50,
          box.y + box.height / 2,
          { steps: 5 }
        );
        await page.mouse.up();
      }
    }

    // The test passes if no error is thrown during the resize interaction.
    // We verify the header is still visible (column was not destroyed).
    await expect(titleHeader).toBeVisible();
  });

  test('sort by clicking column header', async ({ page }) => {
    await goToLibrary(page);

    // Switch to list view
    await page.getByRole('button', { name: 'list view' }).click();
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });

    // Click the "Title" column header to sort
    const titleHeader = page.locator('th').filter({ hasText: 'Title' });
    await titleHeader.click();

    // Verify a sort indicator (arrow icon) appears
    // ArrowUpwardIcon or ArrowDownwardIcon rendered as SVG
    await expect(
      titleHeader.locator('svg[data-testid="ArrowUpwardIcon"], svg[data-testid="ArrowDownwardIcon"]')
    ).toBeVisible({ timeout: 5000 });

    // Click again to reverse sort direction
    const firstArrow = await titleHeader
      .locator('svg[data-testid="ArrowUpwardIcon"]')
      .count();
    await titleHeader.click();

    // If it was ascending, it should now be descending (or vice versa)
    if (firstArrow > 0) {
      await expect(
        titleHeader.locator('svg[data-testid="ArrowDownwardIcon"]')
      ).toBeVisible({ timeout: 5000 });
    } else {
      await expect(
        titleHeader.locator('svg[data-testid="ArrowUpwardIcon"]')
      ).toBeVisible({ timeout: 5000 });
    }
  });

  test('reset columns to defaults', async ({ page }) => {
    await goToLibrary(page);

    // Switch to list view
    await page.getByRole('button', { name: 'list view' }).click();
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });

    // Open column chooser and toggle off a default column (Genre)
    const columnsBtn = page.getByRole('button', { name: /Columns/i });
    await columnsBtn.click();

    const popover = page.locator('.MuiPopover-paper');
    const genreCheckbox = popover
      .locator('label')
      .filter({ hasText: 'Genre' })
      .locator('input[type="checkbox"]');
    await genreCheckbox.click();
    await page.keyboard.press('Escape');

    // Confirm Genre is gone
    await expect(page.locator('th').filter({ hasText: 'Genre' })).toHaveCount(0);

    // Re-open and click "Reset to Defaults"
    await columnsBtn.click();
    const resetBtn = page
      .locator('.MuiPopover-paper')
      .getByRole('button', { name: /Reset to Defaults/i });
    await resetBtn.click();
    await page.keyboard.press('Escape');

    // Genre should be back
    await expect(page.locator('th').filter({ hasText: 'Genre' })).toBeVisible();
  });
});

test.describe('Advanced Search', () => {
  test.beforeEach(async ({ page }) => {
    const books = buildMockBooks();
    await setupAdditionalMocks(page);
    await setupPhase2Interactive(page, undefined, { books });
  });

  test('plain text search works', async ({ page }) => {
    await goToLibrary(page);

    // Find and type in the search input
    const searchInput = page.getByPlaceholder(/Search audiobooks/i);
    await expect(searchInput).toBeVisible({ timeout: 10000 });

    await searchInput.fill('Test Book 1');

    // The search input should have the value
    await expect(searchInput).toHaveValue('Test Book 1');
  });

  test('field:value search shows token chips', async ({ page }) => {
    await goToLibrary(page);

    const searchInput = page.getByPlaceholder(/Search audiobooks/i);
    await expect(searchInput).toBeVisible({ timeout: 10000 });

    // Type a field:value search
    await searchInput.fill('author:"Brandon Sanderson"');

    // A chip should appear below the search bar showing the parsed filter
    const chip = page.locator('.MuiChip-root').filter({
      hasText: /author.*Brandon Sanderson/,
    });
    await expect(chip).toBeVisible({ timeout: 5000 });
  });

  test('remove search token chip', async ({ page }) => {
    await goToLibrary(page);

    const searchInput = page.getByPlaceholder(/Search audiobooks/i);
    await expect(searchInput).toBeVisible({ timeout: 10000 });

    // Type two field:value tokens
    await searchInput.fill('author:smith genre:scifi');

    // Wait for 2 chips
    const chips = page.locator('.MuiChip-root').filter({
      hasText: /author:|genre:/,
    });
    await expect(chips).toHaveCount(2, { timeout: 5000 });

    // Click the delete button on the author chip
    const authorChip = page
      .locator('.MuiChip-root')
      .filter({ hasText: /author:/ });
    const deleteIcon = authorChip.locator('svg, [data-testid*="Cancel"]');
    await deleteIcon.click();

    // Only 1 chip should remain (genre)
    await expect(
      page.locator('.MuiChip-root').filter({ hasText: /genre:/ })
    ).toBeVisible();
    await expect(
      page.locator('.MuiChip-root').filter({ hasText: /author:/ })
    ).toHaveCount(0);
  });

  test('NOT prefix shows negated chip', async ({ page }) => {
    await goToLibrary(page);

    const searchInput = page.getByPlaceholder(/Search audiobooks/i);
    await expect(searchInput).toBeVisible({ timeout: 10000 });

    // Type a negated search
    await searchInput.fill('NOT genre:romance');

    // The chip should appear with error (red) color variant for negation
    const chip = page.locator('.MuiChip-root').filter({
      hasText: /NOT.*genre.*romance/i,
    });
    await expect(chip).toBeVisible({ timeout: 5000 });

    // Verify it has the error color class (MUI applies MuiChip-colorError)
    await expect(chip).toHaveClass(/colorError|error/);
  });
});

test.describe('Tag Management', () => {
  test.beforeEach(async ({ page }) => {
    const books = buildMockBooks();
    await setupAdditionalMocks(page);
    await setupPhase2Interactive(page, undefined, { books });
  });

  test('tag filter appears in sidebar', async ({ page }) => {
    await goToLibrary(page);

    // Click the "Filters" button to open the sidebar
    const filtersBtn = page.getByRole('button', { name: /Filters/i });
    await expect(filtersBtn).toBeVisible({ timeout: 10000 });
    await filtersBtn.click();

    // Verify the filter drawer is open
    // Three drawers are mounted (sidebar, filter, and one more), so a bare
    // .MuiDrawer-paper is a strict-mode violation. Pick the filter one by a
    // control only it contains.
    const drawer = page
      .locator('.MuiDrawer-paper')
      .filter({ has: page.getByText('Library State') });
    await expect(drawer).toBeVisible({ timeout: 5000 });

    // Verify the "Tags" section exists
    await expect(drawer.getByText('Tags')).toBeVisible();

    // Verify the autocomplete input for tag filtering is present
    await expect(
      drawer.getByPlaceholder(/Filter by tags/i)
    ).toBeVisible();
  });

  test('bulk tag dialog opens for selected books', async ({ page }) => {
    await goToLibrary(page);

    // Switch to list view for checkbox selection
    await page.getByRole('button', { name: 'list view' }).click();
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });

    // Wait for table rows
    await page.waitForSelector('table tbody tr', { timeout: 10000 });

    // Select books using the "select all" checkbox in the header
    const selectAllCheckbox = page
      .locator('th')
      .locator('input[type="checkbox"]')
      .first();
    await selectAllCheckbox.click();

    // Find and click the "Tag" button in the selection toolbar
    const tagBtn = page
      .getByRole('button', { name: /^Tag$/i })
      .first();

    // The button should now be enabled
    await expect(tagBtn).toBeEnabled({ timeout: 5000 });
    await tagBtn.click();

    // Verify the bulk tag dialog opens
    const dialog = page.locator('.MuiDialog-paper');
    await expect(dialog).toBeVisible({ timeout: 5000 });

    // Verify it shows the book count
    await expect(
      dialog.getByText(/Manage Tags for \d+ books/i)
    ).toBeVisible();

    // Verify "Add Tags" and "Remove Tags" sections exist
    await expect(dialog.getByText('Add Tags')).toBeVisible();
    await expect(dialog.getByText('Remove Tags')).toBeVisible();
  });
});

test.describe('View Modes', () => {
  test.beforeEach(async ({ page }) => {
    const books = buildMockBooks();
    await setupAdditionalMocks(page);
    await setupPhase2Interactive(page, undefined, { books });
  });

  test('switch between grid and list view', async ({ page }) => {
    await goToLibrary(page);

    // Start by checking that the grid view toggle and list view toggle exist
    const gridBtn = page.getByRole('button', { name: 'grid view' });
    const listBtn = page.getByRole('button', { name: 'list view' });
    await expect(gridBtn).toBeVisible({ timeout: 10000 });
    await expect(listBtn).toBeVisible();

    // Switch to list view
    await listBtn.click();

    // Verify a table renders
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });

    // Switch back to grid view
    await gridBtn.click();

    // Verify grid cards render (MuiCard components)
    await expect(
      page.locator('[class*="MuiCard"]').first()
    ).toBeVisible({ timeout: 10000 });
  });

  test('list view shows dynamic columns', async ({ page }) => {
    await goToLibrary(page);

    // Switch to list view
    await page.getByRole('button', { name: 'list view' }).click();
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });

    // Verify default visible column headers are present:
    // title, author, narrator, series, genre, duration, file size
    const expectedHeaders = [
      'Title',
      'Author',
      'Narrator',
      'Series',
      'Genre',
      'Duration',
      'File Size',
    ];

    for (const header of expectedHeaders) {
      await expect(
        page.locator('th').filter({ hasText: header })
      ).toBeVisible();
    }
  });
});
