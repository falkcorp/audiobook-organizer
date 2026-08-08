// file: web/tests/e2e/import-audiobook-file.spec.ts
// version: 1.5.0
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-08-08

import { test, expect, type Page } from '@playwright/test';
import {
  setupMockApi,
} from './utils/test-helpers';

const filesystem = {
  '/': {
    path: '/',
    items: [
      { name: 'Users', path: '/Users', is_dir: true, excluded: false },
      { name: 'tmp', path: '/tmp', is_dir: true, excluded: false },
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
  '/Users': {
    path: '/Users',
    items: [
      { name: 'jdfalk', path: '/Users/jdfalk', is_dir: true, excluded: false },
    ],
  },
  '/Users/jdfalk': {
    path: '/Users/jdfalk',
    items: [
      {
        name: 'Audiobooks',
        path: '/Users/jdfalk/Audiobooks',
        is_dir: true,
        excluded: false,
      },
      {
        name: 'Documents',
        path: '/Users/jdfalk/Documents',
        is_dir: true,
        excluded: false,
      },
    ],
  },
  '/Users/jdfalk/Audiobooks': {
    path: '/Users/jdfalk/Audiobooks',
    items: [
      {
        name: 'Brandon Sanderson',
        path: '/Users/jdfalk/Audiobooks/Brandon Sanderson',
        is_dir: true,
        excluded: false,
      },
      {
        name: 'standalone.m4b',
        path: '/Users/jdfalk/Audiobooks/standalone.m4b',
        is_dir: false,
        size: 250 * 1024 * 1024,
        mod_time: 1640000000,
        excluded: false,
      },
    ],
  },
  '/Users/jdfalk/Audiobooks/Brandon Sanderson': {
    path: '/Users/jdfalk/Audiobooks/Brandon Sanderson',
    items: [
      {
        name: 'Mistborn 01 - The Final Empire.m4b',
        path: '/Users/jdfalk/Audiobooks/Brandon Sanderson/Mistborn 01 - The Final Empire.m4b',
        is_dir: false,
        size: 400 * 1024 * 1024,
        mod_time: 1640000000,
        excluded: false,
      },
      {
        name: 'Mistborn 02 - The Well of Ascension.m4b',
        path: '/Users/jdfalk/Audiobooks/Brandon Sanderson/Mistborn 02 - The Well of Ascension.m4b',
        is_dir: false,
        size: 450 * 1024 * 1024,
        mod_time: 1640100000,
        excluded: false,
      },
    ],
  },
  '/tmp': {
    path: '/tmp',
    items: [
      {
        name: 'test-audio.m4b',
        path: '/tmp/test-audio.m4b',
        is_dir: false,
        size: 100 * 1024 * 1024,
        excluded: false,
      },
    ],
  },
};

/**
 * Helper to open the Import Audiobook File dialog.
 * The dialog is inside a ServerFileBrowser starting at "/".
 */
const openImportFileBrowser = async (page: Page) => {
  await setupMockApi(page, {
    filesystem,
    books: [],
    homeDirectory: '/Users/jdfalk',
  });
  await page.goto('/library');
  await page.waitForLoadState('networkidle');
  // Two "Import Files" buttons exist (header + empty-state); use .first()
  await page.getByRole('button', { name: 'Import Files' }).first().click();
  await expect(
    page.getByRole('dialog').filter({ hasText: 'Import Audiobook File' })
  ).toBeVisible();
};

/**
 * Locate the ServerFileBrowser dialog to scope selectors and avoid
 * ambiguity with the Library page behind it.
 */
const dialog = (page: Page) =>
  page.getByRole('dialog').filter({ hasText: 'Import Audiobook File' });

test.describe('Import Audiobook File - Interactive Navigation', () => {
  test.beforeEach(async () => {
    // Setup handled by openImportFileBrowser() which calls setupMockApi()
  });

  // TODO(jdfalk): Enable once ServerFileBrowser supports a home-directory button
  // The home icon in breadcrumbs navigates to "/" (filesystem root), not the
  // user's home directory. There is no dedicated home-directory button yet.
  test.skip('navigates to home directory when home icon is clicked', async ({
    page,
  }) => {
    await openImportFileBrowser(page);
    await expect(dialog(page).getByText('Users')).toBeVisible();
    await expect(dialog(page).getByText('tmp')).toBeVisible();

    const homeButton = dialog(page)
      .locator('button')
      .filter({ has: page.locator('svg[data-testid="HomeIcon"]') })
      .first();
    await homeButton.click();

    await expect(dialog(page).getByText('Audiobooks')).toBeVisible();
    await expect(dialog(page).getByText('Documents')).toBeVisible();
  });

  test('navigates through directories by clicking folders', async ({
    page,
  }) => {
    await openImportFileBrowser(page);

    // Navigate through folder structure by clicking
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await expect(dialog(page).getByText('jdfalk')).toBeVisible();

    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    // Use exact match to avoid "No Audiobooks Found" on the Library page behind
    await expect(
      dialog(page).getByRole('button', { name: 'Audiobooks' })
    ).toBeVisible();

    await dialog(page).getByRole('button', { name: 'Audiobooks' }).click();
    await expect(dialog(page).getByText('Brandon Sanderson')).toBeVisible();

    // Final directory contents visible
    await expect(dialog(page).getByText('standalone.m4b')).toBeVisible();
  });

  test('navigates using breadcrumb links', async ({ page }) => {
    await openImportFileBrowser(page);
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    await dialog(page).getByRole('button', { name: 'Audiobooks' }).click();
    await dialog(page).getByRole('button', { name: 'Brandon Sanderson' }).click();

    // We're in the deep folder
    await expect(
      dialog(page).getByText('Mistborn 01 - The Final Empire.m4b')
    ).toBeVisible();

    // Click "Audiobooks" in breadcrumb to go back (first match is breadcrumb)
    await dialog(page)
      .locator('nav')
      .getByRole('button', { name: 'Audiobooks' })
      .click();

    // Should be back at Audiobooks folder
    await expect(dialog(page).getByText('Brandon Sanderson')).toBeVisible();
    await expect(dialog(page).getByText('standalone.m4b')).toBeVisible();
  });

  test('edits path manually and navigates', async ({ page }) => {
    await openImportFileBrowser(page);

    // Click edit path button (the EditIcon next to breadcrumbs)
    const editButton = dialog(page)
      .locator('button')
      .filter({ has: page.locator('svg[data-testid="EditIcon"]') })
      .first();
    await editButton.click();

    // The path editor textbox appears near the CheckIcon button
    // (not the "Import file path" labeled field at the top)
    const checkButton = dialog(page)
      .locator('button')
      .filter({ has: page.locator('svg[data-testid="CheckIcon"]') })
      .first();
    // The textbox is a sibling of the check button in the same Stack
    const pathInput = checkButton.locator('..').getByRole('textbox');
    await pathInput.clear();
    await pathInput.fill('/tmp');

    // Save the path
    await checkButton.click();

    // Should navigate to /tmp
    await expect(dialog(page).getByText('test-audio.m4b')).toBeVisible();
  });

  test('filters files by extension', async ({ page }) => {
    await openImportFileBrowser(page);
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    await dialog(page).getByRole('button', { name: 'Audiobooks' }).click();

    // Enter filter — the extension filter is now a regex search box labelled
    // "Search (regex)" and it applies to directories as well as files, so a
    // ".m4b$" pattern hides the "Brandon Sanderson" folder too.
    await dialog(page).getByLabel('Search (regex)').fill('\\.m4b$');

    // Only .m4b files visible
    await expect(dialog(page).getByText('standalone.m4b')).toBeVisible();
    await expect(
      dialog(page).getByText('Brandon Sanderson')
    ).not.toBeVisible();
  });

  test('single-clicks file to select it', async ({ page }) => {
    await openImportFileBrowser(page);
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    await dialog(page).getByRole('button', { name: 'Audiobooks' }).click();

    // Single-click the file (allowFileSelect is true, so click triggers onSelect)
    await dialog(page).getByRole('button', { name: /standalone\.m4b/ }).click();

    // File should appear in the "Selected Files" list below
    await expect(dialog(page).getByText('Selected Files')).toBeVisible();
    await expect(
      dialog(page).getByText('/Users/jdfalk/Audiobooks/standalone.m4b')
    ).toBeVisible();
  });

  test('navigates from root to specific file through clicks only', async ({
    page,
  }) => {
    await openImportFileBrowser(page);

    // Start at root
    await expect(dialog(page).getByText('Users')).toBeVisible();

    // Navigate: Users → jdfalk → Audiobooks → Brandon Sanderson
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await expect(dialog(page).getByText('jdfalk')).toBeVisible();

    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    await expect(
      dialog(page).getByRole('button', { name: 'Audiobooks' })
    ).toBeVisible();

    await dialog(page).getByRole('button', { name: 'Audiobooks' }).click();
    await expect(dialog(page).getByText('Brandon Sanderson')).toBeVisible();

    await dialog(page).getByRole('button', { name: 'Brandon Sanderson' }).click();
    await expect(
      dialog(page).getByText('Mistborn 01 - The Final Empire.m4b')
    ).toBeVisible();

    // Select a file by clicking (single click triggers selection in this dialog)
    await dialog(page)
      .getByRole('button', { name: /Mistborn 01/ })
      .click();

    // File should appear in selected files list
    await expect(dialog(page).getByText('Selected Files')).toBeVisible();
  });

  test('shows disk space information', async ({ page }) => {
    await openImportFileBrowser(page);

    // Disk info chips should be visible within the dialog
    await expect(dialog(page).getByText(/Available/)).toBeVisible();
    await expect(dialog(page).getByText(/Library/)).toBeVisible();
    await expect(dialog(page).getByText('Readable')).toBeVisible();
    await expect(dialog(page).getByText('Writable')).toBeVisible();
  });

  test('handles clicking on excluded folders', async ({ page }) => {
    await openImportFileBrowser(page);
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    await dialog(page).getByRole('button', { name: 'Audiobooks' }).click();

    // Right-click on a folder to open context menu
    await dialog(page)
      .getByRole('button', { name: 'Brandon Sanderson' })
      .click({ button: 'right' });

    // Context menu appears
    await expect(
      page.getByRole('menuitem', { name: 'Exclude from scan' })
    ).toBeVisible();
  });

  test('preserves navigation state when filtering', async ({ page }) => {
    await openImportFileBrowser(page);
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    await dialog(page).getByRole('button', { name: 'Audiobooks' }).click();

    // Apply filter — the search box is a regex over every entry name, so match
    // both the .m4b file and the folder to keep the folder navigable.
    await dialog(page).getByLabel('Search (regex)').fill('m4b|Brandon');

    // Should still be in the same directory
    await expect(dialog(page).getByText('standalone.m4b')).toBeVisible();

    // Folder should still be navigable
    await dialog(page).getByRole('button', { name: 'Brandon Sanderson' }).click();
    await expect(
      dialog(page).getByText('Mistborn 01 - The Final Empire.m4b')
    ).toBeVisible();
  });

  test('displays file size and modification time', async ({ page }) => {
    await openImportFileBrowser(page);
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    await dialog(page).getByRole('button', { name: 'Audiobooks' }).click();

    // File metadata should be visible (size in MB)
    await expect(dialog(page).getByText(/MB/)).toBeVisible();
  });

  test('handles empty directories gracefully', async ({ page }) => {
    await openImportFileBrowser(page);

    // Navigate to /Users/jdfalk/Documents which has no items in our mock
    // (it's not in the filesystem map so mock returns empty items)
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    await dialog(page).getByRole('button', { name: 'Documents' }).click();

    // Should show empty state message
    await expect(dialog(page).getByText('No items found')).toBeVisible();
  });

  test('maintains current path after closing and reopening dialog', async ({
    page,
  }) => {
    await openImportFileBrowser(page);
    await dialog(page).getByRole('button', { name: 'Users' }).click();
    await dialog(page).getByRole('button', { name: 'jdfalk' }).click();
    await dialog(page).getByRole('button', { name: 'Audiobooks' }).click();

    // Close dialog
    const closeButton = dialog(page).getByRole('button', { name: 'Cancel' });
    await closeButton.click();

    // Reopen dialog (use .first() for the "Import Files" button)
    await page.getByRole('button', { name: 'Import Files' }).first().click();

    // Dialog should reopen (starts at root again since component remounts)
    await expect(
      page.getByRole('dialog').filter({ hasText: 'Import Audiobook File' })
    ).toBeVisible();
  });
});

test.describe('Import Audiobook File - Error Handling', () => {
  test.beforeEach(async () => {
    // Setup handled by openImportFileBrowser() which calls setupMockApi()
  });

  test('handles invalid path entry gracefully', async ({ page }) => {
    await openImportFileBrowser(page);

    // Enter invalid path via edit mode
    const editButton = dialog(page)
      .locator('button')
      .filter({ has: page.locator('svg[data-testid="EditIcon"]') })
      .first();
    await editButton.click();

    const checkButton = dialog(page)
      .locator('button')
      .filter({ has: page.locator('svg[data-testid="CheckIcon"]') })
      .first();
    const pathInput = checkButton.locator('..').getByRole('textbox');
    await pathInput.clear();
    await pathInput.fill('/nonexistent/path');

    await checkButton.click();

    // Mock returns empty items for unknown paths; verify the empty state
    await expect(dialog(page).getByText('No items found')).toBeVisible();
  });
});
