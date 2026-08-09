// file: web/tests/e2e/batch-operations.spec.ts
// version: 1.2.0
// guid: 5d6e7f80-9a0b-1c2d-3e4f-5a6b7c8d9e0f
// last-edited: 2026-08-09

import { test, expect, type Locator, type Page } from '@playwright/test';
import {
  generateTestBooks,
  setupLibraryWithBooks,
  waitForToast,
} from './utils/test-helpers';

const arrangeLibrary = async (page: Page, count = 40) => {
  const books = generateTestBooks(count);
  await setupLibraryWithBooks(page, books);
  await page.goto('/library');
  await page.waitForLoadState('networkidle');
  return books;
};

function getBookCheckboxes(page: Page): Locator {
  return page.getByRole('checkbox', { name: /Select Test Book/ });
}

async function selectFirstBooks(page: Page, count: number): Promise<void> {
  const checkboxes = getBookCheckboxes(page);
  for (let i = 0; i < count; i += 1) {
    await checkboxes.nth(i).click();
  }
}

async function getFirstBookLabel(page: Page): Promise<string> {
  const label = await getBookCheckboxes(page)
    .first()
    .getAttribute('aria-label');
  return label || 'Select Test Book 1';
}

/**
 * NOTE: the "N selected" chip is rendered TWICE in the tree, so a bare
 * getByText('1 selected') is a strict-mode violation ("resolved to 2
 * elements"). These assertions use .first() — the behaviour under test is that
 * the count is displayed, not how many places display it. If the duplication
 * is itself unintended, that is a UI question, not a test one.
 */
test.describe('Batch Operations', () => {
  // Setup handled by arrangeLibrary() → setupLibraryWithBooks() → setupMockApi()
  // (includes skipWelcomeWizard + mockEventSource + setupMockApiRoutes)

  test('selects single book with checkbox', async ({ page }) => {
    // Arrange
    await arrangeLibrary(page);

    // Act
    await selectFirstBooks(page, 1);

    // Assert
    await expect(page.getByText('1 selected').first()).toBeVisible();
  });

  test('selects multiple books with individual checkboxes', async ({
    page,
  }) => {
    // Arrange
    await arrangeLibrary(page);

    // Act
    await selectFirstBooks(page, 5);

    // Assert
    await expect(page.getByText('5 selected').first()).toBeVisible();
  });

  test('selects all books on current page', async ({ page }) => {
    // Arrange
    await arrangeLibrary(page);

    // Act
    await page.getByLabel('Select All').click();

    // Assert
    await expect(page.getByText('20 selected').first()).toBeVisible();
  });

  test('deselects all books', async ({ page }) => {
    // Arrange
    await arrangeLibrary(page);
    await page.getByLabel('Select All').click();

    // Act
    await page.getByRole('button', { name: 'Deselect' }).click();

    // Assert — BatchToolbar returns null at selectedCount === 0
    // (BatchToolbar.tsx:48), so there is no "0 selected" chip to find. Its
    // absence is what an empty selection looks like now.
    await expect(page.getByRole('button', { name: 'Batch Edit' })).toHaveCount(0);
    await expect(page.getByText(/\d+ selected/)).toHaveCount(0);
  });

  test('selection persists across page navigation', async ({ page }) => {
    // Arrange
    await arrangeLibrary(page);
    const firstLabel = await getFirstBookLabel(page);
    await selectFirstBooks(page, 1);

    // Act
    await page.getByRole('button', { name: '2' }).click();
    await page.getByRole('button', { name: '1' }).click();

    // Assert
    await expect(page.getByLabel(firstLabel, { exact: true })).toBeChecked();
  });

  // REMOVED 2026-08-09: the five 'bulk fetch' tests
  //   - bulk fetches metadata for selected books
  //   - monitors bulk fetch progress
  //   - bulk fetch completes successfully and clears selection
  //   - bulk fetch handles partial failures
  //   - cancels bulk fetch operation
  //
  // All five drove a "Bulk Fetch Metadata" dialog with an in-dialog progress
  // counter. That dialog is UNREACHABLE: LibraryDialogs.tsx:920 renders
  // <Dialog open={bulkFetchDialogOpen}>, and setBulkFetchDialogOpen(true) does
  // not appear anywhere in web/src — the state is initialised to false at
  // Library.tsx:352 and only ever set back to false. handleBulkFetchMetadata
  // (Library.tsx:1218) is reachable only from that dead dialog.
  //
  // The flow was replaced by an async one: "Fetch Selected" calls
  // batchFetchCandidates, toasts "Click Review when complete", and a separate
  // "Review" button opens the candidates dialog once the cache is populated.
  // There is no synchronous progress dialog to assert on any more. Rewriting
  // these against the new flow would be new coverage, not repair, so they are
  // removed rather than rewritten. See todo.d/20260809-dead-bulk-fetch-dialog.md.

  test('batch updates metadata field for selected books', async ({ page }) => {
    // Arrange
    await arrangeLibrary(page);
    await selectFirstBooks(page, 3);

    // Act
    await page.getByRole('button', { name: 'Batch Edit' }).click();
    await page.getByRole('checkbox', { name: 'Language' }).check();
    await page.getByPlaceholder('Language').fill('en');
    await page.getByRole('button', { name: 'Update 3 audiobooks' }).click();

    // Assert
    await waitForToast(page, 'Updated metadata for 3 audiobooks.');
  });

  test('batch soft-deletes selected books', async ({ page }) => {
    // Arrange
    await arrangeLibrary(page);
    await selectFirstBooks(page, 2);

    // Act
    await page.getByRole('button', { name: 'Delete Selected' }).click();
    await page.getByRole('button', { name: 'Delete Selected' }).last().click();

    // Assert
    await waitForToast(page, 'Soft deleted 2 selected audiobooks.');
  });

  test('batch restores soft-deleted books', async ({ page }) => {
    // Arrange
    const books = generateTestBooks(5).map((book, index) => ({
      ...book,
      marked_for_deletion: index < 3,
    }));
    await setupLibraryWithBooks(page, books);
    await page.goto('/library');
    await page.waitForLoadState('networkidle');
    await selectFirstBooks(page, 3);

    // Act
    await page.getByRole('button', { name: 'Restore Selected' }).click();

    // Assert
    await waitForToast(page, 'Restored 3 selected audiobooks.');
  });

  test('disables batch operations when no books selected', async ({ page }) => {
    // Arrange
    await arrangeLibrary(page);

    // Assert — batch operations are no longer *disabled* on an empty
    // selection, they are not rendered at all: BatchToolbar.tsx:48 returns
    // null when selectedCount === 0, so the "Select books first" tooltip
    // wrapper this test used to hover no longer exists.
    await expect(page.getByRole('button', { name: 'Batch Edit' })).toHaveCount(0);
    await expect(
      page.getByRole('button', { name: 'Fetch Selected', exact: true })
    ).toHaveCount(0);
  });

  test('shows different batch actions based on selection state', async ({
    page,
  }) => {
    // Arrange
    const books = generateTestBooks(6).map((book, index) => ({
      ...book,
      marked_for_deletion: index % 2 === 0,
    }));
    await setupLibraryWithBooks(page, books);
    await page.goto('/library');
    await page.waitForLoadState('networkidle');
    await selectFirstBooks(page, 2);

    // Assert
    await expect(
      page.getByRole('button', { name: 'Delete Selected' })
    ).toBeEnabled();
    await expect(
      page.getByRole('button', { name: 'Restore Selected' })
    ).toBeEnabled();
  });
});
