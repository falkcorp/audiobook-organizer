// file: web/tests/e2e/error-handling.spec.ts
// version: 1.4.1
// guid: 2f4f5afa-c734-4a00-8a72-d288bcea714f
// last-edited: 2026-08-07

import { test, expect, type Page } from '@playwright/test';
import {
  generateTestBooks,
  setupMockApi,
} from './utils/test-helpers';

const openLibrary = async (
  page: Page,
  options: Parameters<typeof setupMockApi>[1] = {}
) => {
  await setupMockApi(page, options);
  await page.goto('/library');
  await page.waitForLoadState('networkidle');
};

test.describe('Error Handling', () => {
  test.beforeEach(async () => {
    // Setup handled by openLibrary() which calls setupMockApi()
  });

  test('handles network timeout gracefully', async ({ page }) => {
    // Arrange
    await openLibrary(page, { failures: { getBooks: 'timeout' } });

    // Act + Assert
    await expect(page.getByText('Request timed out.')).toBeVisible();
  });

  test('handles 404 not found errors', async ({ page }) => {
    // Arrange
    await setupMockApi(page, { books: [] });

    // Act
    await page.goto('/library/does-not-exist');
    await page.waitForLoadState('networkidle');

    // Assert
    await expect(page.getByText('Audiobook not found.')).toBeVisible();
    await expect(
      page.getByRole('button', { name: 'Back to Library' })
    ).toBeVisible();
  });

  test('handles 500 server errors', async ({ page }) => {
    // Arrange
    await openLibrary(page, { failures: { getBooks: 500 } });

    // Act + Assert
    await expect(page.getByText('Server error occurred.')).toBeVisible();
  });

  test('handles invalid form input', async ({ page }) => {
    // Arrange
    const books = generateTestBooks(1).map((book) => ({
      ...book,
      id: 'book-1',
      title: 'The Way of Kings',
      library_state: 'organized',
    }));
    await setupMockApi(page, { books });

    // Act
    await page.goto('/library/book-1');
    await page.waitForLoadState('networkidle');
    await page.getByRole('button', { name: 'Edit Metadata' }).click();
    await page.getByLabel('Year').fill('abcd');
    await page.getByRole('button', { name: 'Save' }).click();

    // Assert
    await expect(page.getByText('Year must be a number').first()).toBeVisible();
  });

  test('handles concurrent edit conflicts', async ({ page }) => {
    // Arrange
    const books = generateTestBooks(1).map((book) => ({
      ...book,
      id: 'book-1',
      title: 'Conflict Book',
      library_state: 'organized',
      force_update_required: true,
    }));
    await setupMockApi(page, { books });

    // Act
    await page.goto('/library/book-1');
    await page.waitForLoadState('networkidle');
    await page.getByRole('button', { name: 'Edit Metadata' }).click();
    await page.getByLabel('Title').fill('Updated Title');
    await page.getByRole('button', { name: 'Save' }).click();

    // Assert
    await expect(page.getByText('Update Conflict')).toBeVisible();
    await expect(
      page.getByText('Book was updated by another user.')
    ).toBeVisible();
  });

  // TODO(jdfalk): Enable once auth flow is fully integrated - currently
  // the app redirects /login -> /dashboard when requiresLogin is false,
  // so the 401 redirect to /login bounces back to /dashboard.
  test.skip('handles session expiration', async ({ page }) => {
    // Arrange
    await openLibrary(page, { failures: { getBooks: 401 } });

    // Act + Assert
    await page.waitForURL('**/login');
  });

  test('recovers from SSE connection loss', async ({ page }) => {
    // Arrange
    await openLibrary(page);

    // Act
    await page.waitForFunction(() => {
      const mock = (window as unknown as { __mockEventSource?: {
        instances?: unknown[];
      } }).__mockEventSource;
      return Boolean(mock?.instances?.length);
    });
    await page.evaluate(() => {
      const mock = (window as unknown as { __mockEventSource?: {
        instances?: Array<{ emitError?: () => void; onopen?: () => void }>;
      } }).__mockEventSource;
      mock?.instances?.[0]?.emitError?.();
    });

    // Assert
    await expect(page.getByText('Connection lost', { exact: true })).toBeVisible();

    // Act
    await page.evaluate(() => {
      const mock = (window as unknown as { __mockEventSource?: {
        instances?: Array<{ onopen?: () => void }>;
      } }).__mockEventSource;
      mock?.instances?.[0]?.onopen?.();
    });

    // Assert
    await expect(page.getByText('Connection restored.').first()).toBeVisible();
  });

  test('handles file upload errors', async ({ page }) => {
    // Arrange
    await openLibrary(page, { failures: { importFile: 500 } });

    // Act
    await page.getByRole('button', { name: 'Import Files' }).first().click();
    await page.getByLabel('Import file path').fill('/books/broken.m4b');
    await page
      .getByRole('dialog', { name: 'Import Audiobook File' })
      .getByRole('button', { name: 'Import' })
      .click();

    // Assert
    await expect(page.getByText(/Failed to import/i).first()).toBeVisible();
  });
});
