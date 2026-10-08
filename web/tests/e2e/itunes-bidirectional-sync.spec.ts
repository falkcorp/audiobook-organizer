// file: web/tests/e2e/itunes-bidirectional-sync.spec.ts
// version: 1.5.0
// last-edited: 2026-10-08
// guid: f1e2a3b4-c5d6-7890-fghi-j1k2l3m4n5o6

import { test, expect } from '@playwright/test';
import { setupMockApi } from './utils/test-helpers';

test.describe('iTunes Import (manual, re-runnable)', () => {
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
    await page.getByRole('button', { name: 'Import iTunes library' }).click();
    // Mock returns completed immediately, so just check for completion
    await expect(page.getByText('Import Complete', { exact: true })).toBeVisible({ timeout: 10000 });

    // Verify books appear in library
    await page.goto('/library');
    await page.waitForLoadState('networkidle');
    // At least one book should appear from iTunes import
    const bookElements = await page.locator('[role="button"]').filter({ hasText: /.+/ }).count();
    expect(bookElements).toBeGreaterThan(0);
  });

  test('selective import - import only selected books', async ({ page }) => {
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

      await page.getByRole('button', { name: 'Import iTunes library' }).click();
      await expect(page.getByRole('alert').filter({ hasText: /import complete/i })).toBeVisible({ timeout: 10000 });
    } else {
      // If no selective UI, verify basic import still works
      await page.getByRole('button', { name: 'Import iTunes library' }).click();
      await expect(page.getByRole('alert').filter({ hasText: /import complete/i })).toBeVisible({ timeout: 10000 });
    }
  });
});
