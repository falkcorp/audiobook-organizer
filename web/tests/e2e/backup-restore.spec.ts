// file: web/tests/e2e/backup-restore.spec.ts
// version: 1.6.0
// guid: 467b5537-3e41-4190-938c-e2f2ccb2e127
// last-edited: 2026-11-09

import { test, expect, type Page } from '@playwright/test';
import { setupPhase2Interactive } from './utils/test-helpers';

const backups = [
  {
    filename: 'backup-2026-01-25.db.gz',
    size: 25 * 1024 * 1024,
    created_at: '2026-01-25T12:00:00Z',
  },
  {
    filename: 'backup-2026-01-20.db.gz',
    size: 22 * 1024 * 1024,
    created_at: '2026-01-20T12:00:00Z',
    auto: true,
  },
];

const openBackupSettings = async (
  page: Page,
  backupsToUse: unknown[] = [],
  failures: Record<string, unknown> = {}
) => {
  // Phase 2 setup: Reset and skip welcome wizard with mocked APIs (must be before page.goto)
  // This also mocks EventSource to prevent SSE connections
  await setupPhase2Interactive(page, undefined, { backups: backupsToUse, failures });
  // Now navigate to settings
  await page.goto('/settings');
  await page.waitForLoadState('domcontentloaded');
  // Wait for the page to settle and API calls to complete
  await page.waitForLoadState('networkidle');
};

test.describe('Backup and Restore', () => {
  test.beforeEach(async () => {
    // No setup in beforeEach - each test sets up its own mocks
    // This allows each test to use different mock data
  });

  test('creates manual backup', async ({ page }) => {
    // Arrange
    await openBackupSettings(page, []);

    // Act
    await page.getByRole('button', { name: 'Create Backup' }).click();

    // Assert
    await expect(
      page.getByText('Backup created successfully.')
    ).toBeVisible();
    await expect(page.getByText(/backup-/i)).toBeVisible();
  });

  test('lists existing backups', async ({ page }) => {
    // Arrange
    await openBackupSettings(page, backups);

    // Act + Assert
    await expect(page.getByText('backup-2026-01-25.db.gz')).toBeVisible();
    await expect(page.getByText('backup-2026-01-20.db.gz')).toBeVisible();
  });

  test('downloads backup file', async ({ page }) => {
    // Arrange
    await openBackupSettings(page, backups);

    // Act
    const downloadLink = page
      .getByRole('link', { name: 'Download' })
      .first();

    // Assert
    await expect(downloadLink).toHaveAttribute(
      'href',
      '/api/v1/backup/backup-2026-01-25.db.gz'
    );
  });

  test('restores from backup', async ({ page }) => {
    // Arrange
    await openBackupSettings(page, backups);

    // Act
    await page.getByRole('button', { name: 'Restore' }).first().click();
    await expect(page.getByText('Restore Backup')).toBeVisible();
    await page.getByRole('button', { name: 'Restore' }).last().click();

    // Assert: the restore is staged and the server restarts to apply it;
    // the page does not reload itself (the reconnect overlay does, once
    // the server is back).
    await expect(page.getByText('Restore 20261109T010203Z-0a1b2c3d staged.')).toBeVisible();
    await expect(page.getByText('The server is restarting to apply it')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Backups' })).toBeVisible();
  });

  test('deletes backup file', async ({ page }) => {
    // Arrange
    await openBackupSettings(page, backups);

    // Act
    await page.getByRole('button', { name: 'Delete' }).first().click();
    await page.getByRole('button', { name: 'Delete' }).last().click();

    // Assert
    await expect(
      page.getByText('Backup deleted successfully.')
    ).toBeVisible();
  });

  test('validates backup before restore', async ({ page }) => {
    // Arrange
    await openBackupSettings(page, backups, { restoreBackup: 500 });

    // Act
    await page.getByRole('button', { name: 'Restore' }).first().click();
    await page.getByRole('button', { name: 'Restore' }).last().click();

    // Assert
    await expect(page.getByText('Backup file is corrupt.')).toBeVisible();
  });

  test('automatic backup on schedule', async ({ page }) => {
    // Arrange
    await openBackupSettings(page, backups);

    // Act + Assert
    await expect(page.getByText('Auto', { exact: true })).toBeVisible();
  });
});
