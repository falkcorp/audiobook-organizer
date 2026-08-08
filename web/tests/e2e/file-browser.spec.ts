// file: web/tests/e2e/file-browser.spec.ts
// version: 1.5.0
// guid: bbd8bdb0-5dc1-448f-a520-def03ae76825
// last-edited: 2026-08-08

import { test, expect, type Page } from '@playwright/test';
import {
  setupMockApi,
} from './utils/test-helpers';

const filesystem = {
  '/': {
    path: '/',
    items: [
      { name: 'home', path: '/home', is_dir: true, excluded: false },
      { name: 'library', path: '/library', is_dir: true, excluded: false },
      {
        name: 'readme.txt',
        path: '/readme.txt',
        is_dir: false,
        size: 1024,
        excluded: false,
      },
    ],
    disk_info: {
      exists: true,
      readable: true,
      writable: true,
      total_bytes: 500 * 1024 * 1024 * 1024,
      free_bytes: 350 * 1024 * 1024 * 1024,
      library_bytes: 45 * 1024 * 1024 * 1024,
    },
  },
  '/home': {
    path: '/home',
    items: [
      { name: 'user', path: '/home/user', is_dir: true, excluded: false },
    ],
  },
  '/home/user': {
    path: '/home/user',
    items: [
      {
        name: 'audiobooks',
        path: '/home/user/audiobooks',
        is_dir: true,
        excluded: false,
      },
    ],
  },
  '/home/user/audiobooks': {
    path: '/home/user/audiobooks',
    items: [
      {
        name: 'temp',
        path: '/home/user/audiobooks/temp',
        is_dir: true,
        excluded: false,
      },
      {
        name: 'book1.m4b',
        path: '/home/user/audiobooks/book1.m4b',
        is_dir: false,
        size: 1024 * 1024,
        excluded: false,
      },
      {
        name: 'song.mp3',
        path: '/home/user/audiobooks/song.mp3',
        is_dir: false,
        size: 2048,
        excluded: false,
      },
      {
        name: 'notes.txt',
        path: '/home/user/audiobooks/notes.txt',
        is_dir: false,
        size: 512,
        excluded: false,
      },
    ],
  },
  '/home/user/audiobooks/temp': {
    path: '/home/user/audiobooks/temp',
    items: [],
  },
};

const openImportFileBrowser = async (page: Page) => {
  await setupMockApi(page, { filesystem, books: [] });
  await page.goto('/library');
  await page.waitForLoadState('networkidle');
  await page.getByRole('button', { name: 'Import Files' }).first().click();
  await expect(
    page.getByRole('dialog', { name: 'Import Audiobook File' })
  ).toBeVisible();
};

test.describe('File Browser', () => {
  test.beforeEach(async () => {
    // Setup handled by openImportFileBrowser() which calls setupMockApi()
  });

  test('browses root filesystem', async ({ page }) => {
    // Arrange
    await openImportFileBrowser(page);

    // Act + Assert
    await expect(page.getByRole('button', { name: 'home' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'library' })).toBeVisible();
    await expect(page.getByText('readme.txt')).toBeVisible();
  });

  test('navigates into subdirectory', async ({ page }) => {
    // Arrange
    await openImportFileBrowser(page);

    // Act
    await page.getByRole('button', { name: 'home' }).click();
    await page.getByRole('button', { name: 'user' }).click();
    await page.getByRole('button', { name: 'audiobooks' }).click();

    // Assert
    await expect(page.getByText('book1.m4b')).toBeVisible();
    await expect(page.getByText('song.mp3')).toBeVisible();
  });

  test('navigates up directory hierarchy', async ({ page }) => {
    // Arrange
    await openImportFileBrowser(page);
    await page.getByRole('button', { name: 'home' }).click();
    await page.getByRole('button', { name: 'user' }).click();
    await page.getByRole('button', { name: 'audiobooks' }).click();

    // Act
    await page.getByRole('button', { name: 'user' }).first().click();

    // Assert
    await expect(
      page.getByRole('button', { name: 'audiobooks' })
    ).toBeVisible();
  });

  test('creates excluded folder marker', async ({ page }) => {
    // Arrange
    await openImportFileBrowser(page);
    await page.getByRole('button', { name: 'home' }).click();
    await page.getByRole('button', { name: 'user' }).click();
    await page.getByRole('button', { name: 'audiobooks' }).click();

    // Act
    await page.getByRole('button', { name: 'temp' }).click({ button: 'right' });
    await page
      .getByRole('menuitem', { name: 'Exclude from scan' })
      .click();

    // Assert
    await expect(page.getByText('Folder excluded from scan.')).toBeVisible();
    await expect(page.getByText('Excluded', { exact: true })).toBeVisible();
  });

  test('removes excluded folder marker', async ({ page }) => {
    // Arrange
    await openImportFileBrowser(page);
    await page.getByRole('button', { name: 'home' }).click();
    await page.getByRole('button', { name: 'user' }).click();
    await page.getByRole('button', { name: 'audiobooks' }).click();

    // Exclude temp first — use listitem in case button is disabled from prior test state
    await page.getByRole('listitem').filter({ hasText: 'temp' }).click({ button: 'right' });
    // Context menu may show "Exclude" or "Include" depending on current state
    const excludeItem = page.getByRole('menuitem', { name: 'Exclude from scan' });
    const _includeItem = page.getByRole('menuitem', { name: 'Include in scan' });
    if (await excludeItem.isVisible()) {
      await excludeItem.click();
      await expect(page.getByText('Folder excluded from scan.')).toBeVisible();
      await expect(page.getByText('Folder excluded from scan.')).not.toBeVisible({ timeout: 10000 });
    } else {
      // Already excluded, dismiss menu
      await page.keyboard.press('Escape');
    }

    // Now it should be excluded — verify
    await expect(page.getByText('Excluded', { exact: true })).toBeVisible();

    // Act - un-exclude it
    await page.getByRole('listitem').filter({ hasText: 'temp' }).click({ button: 'right' });
    await page.getByRole('menuitem', { name: 'Include in scan' }).click();

    // Assert
    await expect(page.getByText('Folder included in scan.')).toBeVisible();
    await expect(page.getByText('Excluded', { exact: true })).not.toBeVisible();
  });

  test('shows disk space information', async ({ page }) => {
    // Arrange
    await openImportFileBrowser(page);

    // Act + Assert
    await expect(page.getByText(/Available\s+[\d.]+\s+[KMGT]?B/i)).toBeVisible();
    await expect(page.getByText(/Library\s+[\d.]+\s+[KMGT]?B/i)).toBeVisible();
  });

  test('filters files by extension', async ({ page }) => {
    // Arrange
    await openImportFileBrowser(page);
    await page.getByRole('button', { name: 'home' }).click();
    await page.getByRole('button', { name: 'user' }).click();
    await page.getByRole('button', { name: 'audiobooks' }).click();

    // Act — the extension filter is now a regex search box labelled
    // "Search (regex)"; ".m4b" still matches only the .m4b file.
    await page.getByLabel('Search (regex)').fill('\\.m4b$');

    // Assert
    await expect(page.getByText('book1.m4b')).toBeVisible();
    await expect(page.getByText('song.mp3')).not.toBeVisible();
    await expect(page.getByText('notes.txt')).not.toBeVisible();
  });

  test('selects folder as import path', async ({ page }) => {
    // Arrange
    await setupMockApi(page, { filesystem, importPaths: [] });
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');

    // Act — import paths moved onto a dedicated "Paths" tab in Settings.
    await page.getByRole('tab', { name: 'Paths' }).click();
    await page.getByRole('button', { name: 'Add Import Path' }).click();
    await page
      .getByRole('button', { name: 'Browse Server Filesystem' })
      .click();
    await page.getByRole('button', { name: 'home' }).click();
    await page.getByRole('button', { name: 'user' }).click();
    await page.getByRole('button', { name: 'audiobooks' }).click();
    await page.getByRole('button', { name: 'Add Path' }).click();

    // Assert
    await expect(page.getByText('/home/user/audiobooks')).toBeVisible();
  });
});
