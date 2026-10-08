// file: tests/e2e/itunes-import.spec.ts
// version: 1.4.0
// guid: 8d0eb913-029f-42f1-ad7b-984aa66a6fdc
// last-edited: 2026-10-08

import { test, expect } from '@playwright/test';
import { setupMockApi } from './utils/test-helpers';

test.describe('iTunes Import', () => {
  test.beforeEach(async ({ page }) => {
    await setupMockApi(page);
  });

  test('validates iTunes library', async ({ page }) => {
    // Act
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'iTunes Import' }).click();
    await page
      .getByLabel('iTunes Library Path')
      .fill('/path/to/test/library.xml');
    await page.getByRole('button', { name: 'Validate Import' }).click();

    // Assert
    await expect(page.getByText('Validation Results')).toBeVisible();
  });

  test('imports iTunes library', async ({ page }) => {
    // Act
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'iTunes Import' }).click();
    await page
      .getByLabel('iTunes Library Path')
      .fill('/path/to/test/library.xml');
    await page.getByRole('button', { name: 'Validate Import' }).click();
    await expect(page.getByText('Validation Results')).toBeVisible();

    await page.getByRole('button', { name: 'Import iTunes library' }).click();

    // Assert - mock returns completed immediately so progressbar may be brief
    // Just verify the import completes successfully
    await expect(page.getByText('Import Complete', { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId('itunes-import-result')).toHaveText(
      'Linked 0, added 11, skipped 1'
    );
  });
});

// Import is the only iTunes action and can be re-run. With books already
// linked to iTunes, a repeat run is confirmed first.
test.describe('iTunes Import again', () => {
  const warning =
    "You've imported from iTunes before. We match each album to your existing books by " +
    'iTunes ID, then by file path. Albums iTunes has re-created with new IDs, or whose files ' +
    "moved, can't be matched and will be added as new books, so you may see duplicates. " +
    'Nothing in iTunes is changed, and no files are moved.';

  test.beforeEach(async ({ page }) => {
    await setupMockApi(page, { itunes: { linkedBookCount: 3 } });
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'iTunes Import' }).click();
    await page.getByLabel('iTunes Library Path').fill('/path/to/test/library.xml');
    await page.getByRole('button', { name: 'Validate Import' }).click();
    await expect(page.getByText('Validation Results')).toBeVisible();
  });

  test('warns before importing again, and Import anyway runs it', async ({ page }) => {
    await page.getByRole('button', { name: 'Import iTunes library' }).click();

    const dialog = page.getByRole('dialog', { name: 'Import iTunes library again?' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText(warning, { exact: true })).toBeVisible();

    await dialog.getByRole('button', { name: 'Import anyway' }).click();
    await expect(page.getByText('Import Complete', { exact: true })).toBeVisible({ timeout: 10000 });
  });

  test('Cancel starts nothing', async ({ page }) => {
    let importCalls = 0;
    page.on('request', (req) => {
      if (req.method() === 'POST' && new URL(req.url()).pathname === '/api/v1/itunes/import') {
        importCalls++;
      }
    });
    await page.getByRole('button', { name: 'Import iTunes library' }).click();
    const dialog = page.getByRole('dialog', { name: 'Import iTunes library again?' });
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
    expect(importCalls).toBe(0);
    await expect(page.getByText('Import Complete', { exact: true })).toBeHidden();
  });
});
