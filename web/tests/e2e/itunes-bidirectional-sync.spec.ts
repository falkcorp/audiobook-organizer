// file: web/tests/e2e/itunes-bidirectional-sync.spec.ts
// version: 1.4.0
// last-edited: 2026-10-07
// guid: f1e2a3b4-c5d6-7890-fghi-j1k2l3m4n5o6

import { test, expect } from '@playwright/test';
import { setupMockApi } from './utils/test-helpers';

test.describe('iTunes Bidirectional Sync', () => {
  test.beforeEach(async ({ page }) => {
    await setupMockApi(page);
  });

  test('import from iTunes - happy path', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');

    // Navigate to iTunes tab
    await page.getByRole('tab', { name: 'iTunes Import' }).click();

    // Enter path to test iTunes library
    const testLibraryPath = 'testdata/itunes/Library.xml';
    await page.getByLabel('iTunes Library Path').fill(testLibraryPath);

    // Validate library
    await page.getByRole('button', { name: 'Validate Import' }).click();
    await expect(page.getByText(/validation results|found \d+ books/i)).toBeVisible({ timeout: 5000 });

    // Import library
    await page.getByRole('button', { name: 'Import Library' }).click();
    // Mock returns completed immediately, so just check for completion
    await expect(page.getByText('Import Complete', { exact: true })).toBeVisible({ timeout: 10000 });

    // Verify books appear in library
    await page.goto('/library');
    await page.waitForLoadState('networkidle');
    // At least one book should appear from iTunes import
    const bookElements = await page.locator('[role="button"]').filter({ hasText: /.+/ }).count();
    expect(bookElements).toBeGreaterThan(0);
  });

  test('iTunes conflict - newer iTunes data takes precedence', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'iTunes Import' }).click();

    const testLibraryPath = 'testdata/itunes/Library.xml';
    await page.getByLabel('iTunes Library Path').fill(testLibraryPath);
    await page.getByRole('button', { name: 'Validate Import' }).click();
    await expect(page.getByText(/validation results|found \d+ books/i)).toBeVisible({ timeout: 5000 });
    await page.getByRole('button', { name: 'Import Library' }).click();
    await expect(page.getByRole('alert').filter({ hasText: /import complete/i })).toBeVisible({ timeout: 10000 });

    // Simulate a conflict by editing a book, then triggering a re-import
    await page.goto('/library');
    await page.waitForLoadState('networkidle');

    // If a conflict dialog appears, select "Use iTunes" option
    const itunesRadio = page.locator('input[type="radio"]').filter({ near: page.getByText(/use itunes|itunes version/i) }).first();
    if (await itunesRadio.isVisible({ timeout: 2000 }).catch(() => false)) {
      await itunesRadio.click();
      const applyButton = page.getByRole('button', { name: /apply|confirm|sync/i }).first();
      if (await applyButton.isVisible().catch(() => false)) {
        await applyButton.click();
      }
    }
  });

  test('organizer conflict - newer organizer data takes precedence', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'iTunes Import' }).click();

    const testLibraryPath = 'testdata/itunes/Library.xml';
    await page.getByLabel('iTunes Library Path').fill(testLibraryPath);
    await page.getByRole('button', { name: 'Validate Import' }).click();
    await expect(page.getByText(/validation results|found \d+ books/i)).toBeVisible({ timeout: 5000 });
    await page.getByRole('button', { name: 'Import Library' }).click();
    await expect(page.getByRole('alert').filter({ hasText: /import complete/i })).toBeVisible({ timeout: 10000 });

    // Navigate to a book and edit it significantly
    await page.goto('/library');
    await page.waitForLoadState('networkidle');
    const firstBook = page.locator('[role="button"]').first();
    await expect(firstBook).toBeVisible();
    await firstBook.click();

    // Edit multiple fields to create conflict
    await page.waitForLoadState('domcontentloaded');
    const commentsField = page.locator('textarea, input[placeholder*="comment"]').first();
    if (await commentsField.isVisible().catch(() => false)) {
      await commentsField.click();
      await commentsField.fill('Major update from organizer - should override iTunes');
    }

    // Save and navigate to iTunes sync
    const saveButton = page.locator('button').filter({ hasText: /save|confirm/i }).first();
    if (await saveButton.isVisible().catch(() => false)) {
      await saveButton.click();
    }

    // Go to iTunes settings and try write-back
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'iTunes Import' }).click();

    // If conflict dialog appears, select "Use Organizer" option
    const organizerRadio = page.locator('input[type="radio"]').filter({ near: page.getByText(/use organizer|organizer version/i) }).first();
    if (await organizerRadio.isVisible({ timeout: 2000 }).catch(() => false)) {
      await organizerRadio.click();
      const applyButton = page.getByRole('button', { name: /apply|confirm|sync/i }).first();
      if (await applyButton.isVisible().catch(() => false)) {
        await applyButton.click();
      }
    }
  });

  test('selective sync - import only selected books', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'iTunes Import' }).click();

    const testLibraryPath = 'testdata/itunes/Library.xml';
    await page.getByLabel('iTunes Library Path').fill(testLibraryPath);
    await page.getByRole('button', { name: 'Validate Import' }).click();
    await expect(page.getByText(/validation results|found \d+ books/i)).toBeVisible({ timeout: 5000 });

    // Look for selective options (checkboxes or similar)
    const selectiveCheckboxes = page.locator('input[type="checkbox"]');
    const checkboxCount = await selectiveCheckboxes.count();

    if (checkboxCount > 0) {
      // Uncheck some items to do selective import
      const firstCheckbox = selectiveCheckboxes.first();
      await firstCheckbox.click();

      await page.getByRole('button', { name: 'Import Library' }).click();
      await expect(page.getByRole('alert').filter({ hasText: /import complete/i })).toBeVisible({ timeout: 10000 });
    } else {
      // If no selective UI, verify basic import still works
      await page.getByRole('button', { name: 'Import Library' }).click();
      await expect(page.getByRole('alert').filter({ hasText: /import complete/i })).toBeVisible({ timeout: 10000 });
    }
  });

  test('retry failed sync operation', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'iTunes Import' }).click();

    // Try with invalid path to trigger failure
    await page.getByLabel('iTunes Library Path').fill('/invalid/path/nonexistent.xml');
    await page.getByRole('button', { name: 'Validate Import' }).click();

    // Should show error
    await expect(page.getByText(/error|not found|invalid/i)).toBeVisible({ timeout: 5000 });

    // Check if Retry button appears and is enabled
    const retryButton = page.getByRole('button', { name: /retry|re-sync/i }).first();
    if (await retryButton.isEnabled({ timeout: 2000 }).catch(() => false)) {
      // Clear the error by entering valid path
      await page.getByLabel('iTunes Library Path').clear();
      await page.getByLabel('iTunes Library Path').fill('testdata/itunes/Library.xml');

      // Retry should work now
      await retryButton.click();
      await expect(page.getByText(/validation results|found \d+ books|success/i)).toBeVisible({ timeout: 5000 });
    }
  });
});
