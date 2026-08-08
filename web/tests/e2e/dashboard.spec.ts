// file: web/tests/e2e/dashboard.spec.ts
// version: 1.3.1
// guid: f6e23777-438b-4931-88d9-d2c6d2225a00
// last-edited: 2026-08-07

import { test, expect, type Page } from '@playwright/test';
import {
  generateTestBooks,
  setupMockApi,
} from './utils/test-helpers';

const buildBooks = () => {
  const base = generateTestBooks(1)[0];
  return [
    {
      ...base,
      id: 'book-1',
      title: 'Book 1',
      author_name: 'Author A',
      series_name: 'Series X',
      library_state: 'organized',
      file_size: 45 * 1024 * 1024 * 1024,
    },
    {
      ...base,
      id: 'book-2',
      title: 'Book 2',
      author_name: 'Author B',
      series_name: 'Series X',
      library_state: 'organized',
      file_size: 0,
    },
    {
      ...base,
      id: 'book-3',
      title: 'Book 3',
      author_name: 'Author C',
      series_name: 'Series Y',
      library_state: 'organized',
      file_size: 0,
    },
    {
      ...base,
      id: 'book-4',
      title: 'Book 4',
      author_name: 'Author A',
      series_name: null,
      library_state: 'import',
      file_size: 0,
    },
    {
      ...base,
      id: 'book-5',
      title: 'Book 5',
      author_name: 'Author B',
      series_name: null,
      library_state: 'import',
      file_size: 0,
    },
  ];
};

const openDashboard = async (page: Page) => {
  await setupMockApi(page, {
    books: buildBooks(),
    operations: {
      history: [
        {
          id: 'op-1',
          type: 'scan',
          status: 'completed',
          progress: 100,
          total: 100,
          message: 'Scan completed',
          created_at: '2026-01-25T10:00:00Z',
        },
        {
          id: 'op-2',
          type: 'organize',
          status: 'completed',
          progress: 10,
          total: 10,
          message: 'Organize completed',
          created_at: '2026-01-25T09:00:00Z',
        },
      ],
    },
    systemStatus: {
      disk_total_bytes: 500 * 1024 * 1024 * 1024,
    },
  });
  await page.goto('/');
  await page.waitForLoadState('networkidle');
};

test.describe('Dashboard', () => {
  test.beforeEach(async () => {
    // Setup handled by openDashboard() which calls setupMockApi()
  });

  test('displays library statistics', async ({ page }) => {
    // Arrange
    await openDashboard(page);

    // Act + Assert
    const libraryCard = page.getByText('Library Books').locator('..');
    await expect(libraryCard).toBeVisible();
    await expect(libraryCard.getByText('5')).toBeVisible();
  });

  test('displays import paths statistics', async ({ page }) => {
    // Arrange
    await openDashboard(page);

    // Act + Assert
    const importCard = page.getByText('Import Path Books').locator('..');
    await expect(importCard).toBeVisible();
    await expect(importCard.getByText('2')).toBeVisible();
  });

  test('displays recent operations', async ({ page }) => {
    // Arrange
    await openDashboard(page);

    // Act + Assert
    await expect(page.getByText('Scan completed')).toBeVisible();
    await expect(page.getByText('Organize completed')).toBeVisible();
  });

  test('displays storage usage', async ({ page }) => {
    // Arrange
    await openDashboard(page);

    // Act + Assert
    await expect(page.getByText('45.0 GB / 500.0 GB')).toBeVisible();
    await expect(page.getByText('9% of disk used')).toBeVisible();
  });

  test('quick action: start scan', async ({ page }) => {
    // Arrange
    await openDashboard(page);

    // Act
    await page.getByRole('button', { name: 'Scan All Import Paths' }).click();

    // Assert
    await expect(page).toHaveURL(/\/operations/);
  });

  test('quick action: organize all import books', async ({ page }) => {
    // Arrange
    await openDashboard(page);

    // Act
    await page.getByRole('button', { name: 'Organize All' }).click();
    await page
      .getByRole('dialog', { name: 'Organize All Scanned Books' })
      .getByRole('button', { name: 'Organize' })
      .click();

    // Assert
    await expect(page).toHaveURL(/\/operations/);
  });
});
