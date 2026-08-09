// file: web/tests/e2e/batch-operations.spec.ts
// version: 1.1.0
// guid: 5d6e7f80-9a0b-1c2d-3e4f-5a6b7c8d9e0f
// last-edited: 2026-02-04

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

    // Assert
    await expect(page.getByText('0 selected').first()).toBeVisible();
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

  test('bulk fetches metadata for selected books', async ({ page }) => {
    // Arrange
    await arrangeLibrary(page);
    await selectFirstBooks(page, 3);

    // Act
    await page
      .getByRole('button', { name: 'Fetch Selected', exact: true })
      .click();
    await page
      .getByRole('dialog', { name: 'Bulk Fetch Metadata' })
      .getByRole('button', { name: 'Fetch Metadata', exact: true })
      .click();

    // Assert
    await expect(page.getByText('3 / 3 completed')).toBeVisible();
    await waitForToast(page, 'Metadata fetched for 3 books.');
  });

  test('monitors bulk fetch progress', async ({ page }) => {
    // Arrange
    await arrangeLibrary(page);
    await page.getByLabel('Select All').click();

    // Act
    await page
      .getByRole('button', { name: 'Fetch Selected', exact: true })
      .click();
    await page
      .getByRole('dialog', { name: 'Bulk Fetch Metadata' })
      .getByRole('button', { name: 'Fetch Metadata', exact: true })
      .click();

    // Assert
    const bulkDialog = page.getByRole('dialog', {
      name: 'Bulk Fetch Metadata',
    });
    await expect(
      bulkDialog.getByText('20 / 20 completed', { exact: true })
    ).toBeVisible();
    await expect(
      bulkDialog.getByText('Test Book 1', { exact: true })
    ).toBeVisible();
  });

  test('bulk fetch completes successfully and clears selection', async ({
    page,
  }) => {
    // Arrange
    await arrangeLibrary(page);
    await selectFirstBooks(page, 1);

    // Act
    await page
      .getByRole('button', { name: 'Fetch Selected', exact: true })
      .click();
    await page
      .getByRole('dialog', { name: 'Bulk Fetch Metadata' })
      .getByRole('button', { name: 'Fetch Metadata', exact: true })
      .click();

    // Assert
    await waitForToast(page, 'Metadata fetched for 1 books.');
    await expect(page.getByText('0 selected', { exact: true })).toBeVisible();
  });

  test('bulk fetch handles partial failures', async ({ page }) => {
    // Arrange
    const books = generateTestBooks(10);
    books[1].fetch_metadata_error = true;
    books[3].fetch_metadata_error = true;
    await setupLibraryWithBooks(page, books);
    await page.goto('/library');
    await page.waitForLoadState('networkidle');
    await selectFirstBooks(page, 5);

    // Act
    await page
      .getByRole('button', { name: 'Fetch Selected', exact: true })
      .click();
    await page
      .getByRole('dialog', { name: 'Bulk Fetch Metadata' })
      .getByRole('button', { name: 'Fetch Metadata', exact: true })
      .click();

    // Assert
    await waitForToast(page, '3 succeeded, 2 failed.');
    await expect(page.getByText('Metadata fetch failed').first()).toBeVisible();
  });

  test('cancels bulk fetch operation', async ({ page }) => {
    // Arrange
    const books = generateTestBooks(15).map((book) => ({
      ...book,
      fetch_metadata_delay_ms: 200,
    }));
    await setupLibraryWithBooks(page, books);
    await page.goto('/library');
    await page.waitForLoadState('networkidle');
    await page.getByLabel('Select All').click();

    // Act
    await page
      .getByRole('button', { name: 'Fetch Selected', exact: true })
      .click();
    await page
      .getByRole('dialog', { name: 'Bulk Fetch Metadata' })
      .getByRole('button', { name: 'Fetch Metadata', exact: true })
      .click();
    await page.getByRole('button', { name: 'Cancel' }).click();

    // Assert
    await waitForToast(page, 'Bulk fetch cancelled.');
  });

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

    // Act
    await page
      .locator('span[aria-label="Select books first"]')
      .filter({ hasText: 'Batch Edit' })
      .hover();

    // Assert
    await expect(page.getByText('Select books first')).toBeVisible();
    await expect(
      page.getByRole('button', { name: 'Batch Edit' })
    ).toBeDisabled();
    await expect(
      page.getByRole('button', { name: 'Fetch Selected', exact: true })
    ).toBeDisabled();
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
