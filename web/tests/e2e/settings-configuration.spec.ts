// file: web/tests/e2e/settings-configuration.spec.ts
// version: 1.3.0
// guid: ab83d28e-beb5-4288-821f-7bf82704f4b9
// last-edited: 2026-08-09

import { test, expect, type Page } from '@playwright/test';
import {
  setupMockApi,
} from './utils/test-helpers';

const BLOCKED_HASH = 'a'.repeat(64);

const baseFilesystem = {
  '/': {
    path: '/',
    items: [
      { name: 'home', path: '/home', is_dir: true, excluded: false },
      { name: 'library', path: '/library', is_dir: true, excluded: false },
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
    items: [],
  },
};

const openSettings = async (
  page: Page,
  options: Parameters<typeof setupMockApi>[1] = {}
) => {
  await setupMockApi(page, options);
  await page.goto('/settings');
  await page.waitForLoadState('networkidle');
};

test.describe('Settings Configuration', () => {
  test.beforeEach(async () => {
    // Note: beforeEach doesn't do setup here
    // Each test calls openSettings() which handles all mock setup
    // This prevents conflicts between Phase 1 (real API) and Phase 2 (mocked API)
  });

  test('loads settings page with all sections', async ({ page }) => {
    // Arrange
    await openSettings(page, {
      blockedHashes: [
        {
          hash: BLOCKED_HASH,
          reason: 'Duplicate',
          created_at: '2026-01-25T12:00:00Z',
        },
      ],
    });

    // Act + Assert
    await expect(page.getByText('Library Settings')).toBeVisible();
    await expect(page.getByText('Scan Settings')).toBeVisible();

    // Import paths moved off the Library tab onto their own "Paths" tab
    // (PathsSettingsTab.tsx:108). The Library tab now just points at it — its
    // copy reads "Import paths are configured in File Manager."
    await page.getByRole('tab', { name: 'Paths' }).click();
    await expect(
      page.getByText('Import Paths (Watch Locations)')
    ).toBeVisible();

    await page.getByRole('tab', { name: 'Metadata' }).click();
    await expect(page.getByText('Metadata Settings')).toBeVisible();

    await page.getByRole('tab', { name: 'Security' }).click();
    await expect(page.getByText('Blocked File Hashes')).toBeVisible();

    await page.getByRole('tab', { name: 'System Info' }).click();
    await expect(page.getByText('System Information')).toBeVisible();
  });

  test('updates library root directory', async ({ page }) => {
    // Arrange
    await openSettings(page, {
      config: { root_dir: '/library' },
    });

    // Act
    await page.getByLabel('Library Path').fill('/new/library/path');
    await page.getByRole('button', { name: 'Save Settings' }).click();

    // Assert
    await expect(page.getByText('Settings saved successfully!')).toBeVisible();

    await page.reload();
    await page.waitForLoadState('networkidle');
    await expect(page.getByLabel('Library Path')).toHaveValue(
      '/new/library/path'
    );
  });

  test('browses for library root directory', async ({ page }) => {
    // Arrange
    await openSettings(page, {
      config: { root_dir: '/' },
      filesystem: baseFilesystem,
    });

    // Act
    await page.getByRole('button', { name: 'Browse Server' }).click();
    await expect(
      page.getByRole('dialog', { name: 'Browse Server Filesystem' })
    ).toBeVisible();

    await page.getByText('home', { exact: true }).click();
    await page.getByText('user', { exact: true }).click();
    await page.getByText('audiobooks', { exact: true }).click();

    await page.getByRole('button', { name: 'Select Folder' }).click();

    // Assert
    await expect(page.getByLabel('Library Path')).toHaveValue(
      '/home/user/audiobooks'
    );
  });

  test('updates OpenAI API key', async ({ page }) => {
    // Arrange
    await openSettings(page, {
      config: { openai_api_key: '' },
    });

    // Act
    await page.getByRole('tab', { name: 'Metadata' }).click();
    await page
      .getByLabel('Enable AI-powered filename parsing')
      .click();
    await page.getByLabel('OpenAI API Key').fill('sk-test1234');
    await page.getByRole('button', { name: 'Save Settings' }).click();

    // Assert
    await expect(page.getByText('Settings saved successfully!')).toBeVisible();
    await expect(page.getByLabel('OpenAI API Key')).toHaveValue('');
    await expect(page.getByPlaceholder(/key saved/i)).toBeVisible();
  });

  test('tests OpenAI connection', async ({ page }) => {
    // Arrange
    await openSettings(page);

    // Act
    await page.getByRole('tab', { name: 'Metadata' }).click();
    await page
      .getByLabel('Enable AI-powered filename parsing')
      .click();
    await page.getByLabel('OpenAI API Key').fill('sk-test1234');
    await page.getByRole('button', { name: 'Test Connection' }).click();

    // Assert
    await expect(page.getByText('Connection successful')).toBeVisible();
  });

  test('handles OpenAI connection failure', async ({ page }) => {
    // Arrange
    await openSettings(page, {
      failures: { openaiTest: 401 },
    });

    // Act
    await page.getByRole('tab', { name: 'Metadata' }).click();
    await page
      .getByLabel('Enable AI-powered filename parsing')
      .click();
    await page.getByLabel('OpenAI API Key').fill('sk-test1234');
    await page.getByRole('button', { name: 'Test Connection' }).click();

    // Assert
    await expect(page.getByText('Connection failed')).toBeVisible();
  });

  test('configures scan settings: file extensions', async ({ page }) => {
    // Arrange
    await openSettings(page);

    // Act
    await page.getByLabel('Add extension').fill('.opus');
    await page.getByRole('button', { name: 'Add' }).first().click();
    await page.getByRole('button', { name: 'Save Settings' }).click();

    // Assert
    await expect(page.getByText('Settings saved successfully!')).toBeVisible();
    await page.reload();
    await page.waitForLoadState('networkidle');
    await expect(page.getByText('.opus')).toBeVisible();
  });

  test('configures scan settings: exclude patterns', async ({ page }) => {
    // Arrange
    await openSettings(page);

    // Act
    await page.getByLabel('Add exclude pattern').fill('*_preview.m4b');
    // Click the Add button next to the exclude pattern input (not the Add Import Path button)
    const excludeSection = page.getByText('Exclude Patterns').locator('..');
    await excludeSection.getByRole('button', { name: 'Add' }).click();
    await page.getByRole('button', { name: 'Save Settings' }).click();

    // Assert
    await expect(page.getByText('Settings saved successfully!')).toBeVisible();
    await page.reload();
    await page.waitForLoadState('networkidle');
    await expect(page.getByText('*_preview.m4b')).toBeVisible();
  });

  test('views blocked hashes list', async ({ page }) => {
    // Arrange
    await openSettings(page, {
      blockedHashes: [
        {
          hash: BLOCKED_HASH,
          reason: 'Duplicate',
          created_at: '2026-01-25T12:00:00Z',
        },
      ],
    });

    // Act
    await page.getByRole('tab', { name: 'Security' }).click();

    // Assert
    await expect(page.getByText('Duplicate')).toBeVisible();
  });

  test('adds hash to blocked list manually', async ({ page }) => {
    // Arrange
    await openSettings(page, { blockedHashes: [] });

    // Act
    await page.getByRole('tab', { name: 'Security' }).click();
    await page.getByRole('button', { name: 'Block Hash' }).click();
    await page.getByLabel('File Hash (SHA256)').fill(BLOCKED_HASH);
    await page.getByLabel('Reason').fill('Duplicate');
    await page.getByRole('button', { name: 'Block Hash' }).last().click();

    // Assert
    await expect(page.getByText('Hash blocked successfully')).toBeVisible();
    await expect(page.getByText('Duplicate')).toBeVisible();
  });

  test('removes hash from blocked list', async ({ page }) => {
    // Arrange
    await openSettings(page, {
      blockedHashes: [
        {
          hash: BLOCKED_HASH,
          reason: 'Duplicate',
          created_at: '2026-01-25T12:00:00Z',
        },
      ],
    });

    // Act
    await page.getByRole('tab', { name: 'Security' }).click();
    await page.getByTitle('Unblock this hash').click();
    await page.getByRole('button', { name: 'Unblock' }).click();

    // Assert
    await expect(page.getByText('Hash unblocked successfully')).toBeVisible();
  });

  test('views system information', async ({ page }) => {
    // Arrange
    await openSettings(page);

    // Act
    await page.getByRole('tab', { name: 'System Info' }).click();

    // Assert
    await expect(page.getByText('Operating System')).toBeVisible();
    await expect(page.getByText('Go Version')).toBeVisible();
  });

  test('exports settings configuration', async ({ page }) => {
    // Arrange
    await openSettings(page);

    // Act
    await page.getByRole('button', { name: 'Export Settings' }).click();

    // Assert
    await expect(page.getByText('Settings exported.')).toBeVisible();
  });

  test('imports settings configuration', async ({ page }) => {
    // Arrange
    await openSettings(page);

    const importPayload = {
      root_dir: '/imported/library',
      supported_extensions: ['.m4b', '.mp3'],
      exclude_patterns: ['*_preview.m4b'],
    };

    // Act
    await page.getByRole('button', { name: 'Import Settings' }).click();
    const input = page.locator('input[type="file"]');
    await input.setInputFiles({
      name: 'settings.json',
      mimeType: 'application/json',
      buffer: Buffer.from(JSON.stringify(importPayload)),
    });

    await expect(page.getByRole('dialog')).toBeVisible();
    await page.getByRole('dialog').getByRole('button', { name: 'Import Settings' }).click();

    // Assert
    await expect(
      page.getByText('Settings imported successfully.')
    ).toBeVisible();
    await expect(page.getByLabel('Library Path')).toHaveValue(
      '/imported/library'
    );
  });

  test('resets settings to defaults', async ({ page }) => {
    // Arrange
    await openSettings(page);

    // Act
    await page.getByLabel('Library Path').fill('/custom/path');
    page.once('dialog', (dialog) => dialog.accept());
    await page.getByRole('button', { name: 'Reset to Defaults' }).click();

    // Assert
    await expect(page.getByLabel('Library Path')).toHaveValue('/path/to/audiobooks/library');
  });

  test('shows unsaved changes warning', async ({ page }) => {
    // Arrange
    await openSettings(page);

    // Act
    await page.getByLabel('Library Path').fill('/unsaved/path');
    await page.getByRole('button', { name: 'Library' }).click();

    // Assert
    await expect(page.getByText('Unsaved Changes').first()).toBeVisible();
    await expect(page.getByRole('button', { name: 'Save' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Discard' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Cancel' })).toBeVisible();
  });

  test('validates root directory exists', async ({ page }) => {
    // Arrange
    await openSettings(page, {
      failures: { filesystem: 404 },
    });

    // Act
    await page.getByLabel('Library Path').fill('/fake/path');
    await page.getByRole('button', { name: 'Save Settings' }).click();

    // Assert
    await expect(page.getByText('Directory does not exist.')).toBeVisible();
  });

  test('validates API key format', async ({ page }) => {
    // Arrange
    await openSettings(page);

    // Act
    await page.getByRole('tab', { name: 'Metadata' }).click();
    await page
      .getByLabel('Enable AI-powered filename parsing')
      .click();
    await page.getByLabel('OpenAI API Key').fill('invalid');
    await page.getByRole('button', { name: 'Save Settings' }).click();

    // Assert
    await expect(page.getByText('Invalid API key format.')).toBeVisible();
  });
});
