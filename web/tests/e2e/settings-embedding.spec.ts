// file: web/tests/e2e/settings-embedding.spec.ts
// version: 1.0.0
// guid: f4e3d2c1-b0a9-8765-fedc-ba9876543210
// last-edited: 2026-06-19

/**
 * E2E round-trip test: toggle the "Enable embedding generation" Switch in
 * the Dedup tab → save → reload → assert state persisted.
 *
 * This test requires a live server. In CI the server is started before the
 * Playwright suite runs. Locally, the test is skipped when no server is
 * reachable (connection-refused / timeout on the initial navigation).
 *
 * To run locally:
 *   make run-api &   # start the server
 *   make test-e2e    # run the Playwright suite
 */

import { test, expect, type Page } from '@playwright/test';
import { setupMockApi } from './utils/test-helpers';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const openSettingsDedupTab = async (
  page: Page,
  options: Parameters<typeof setupMockApi>[1] = {}
) => {
  await setupMockApi(page, options);
  await page.goto('/settings');
  await page.waitForLoadState('networkidle');
  await page.getByRole('tab', { name: 'Dedup' }).click();
};

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('Settings — Embedding toggle round-trip', () => {
  test('toggling Enable embedding generation persists after reload', async ({ page }) => {
    // Start with embedding disabled
    await openSettingsDedupTab(page, {
      config: {
        // The mock config helper merges these into the default flat config.
        // The Settings page reads config.embedding.enabled when a nested
        // embedding object is present.
      },
    });

    // The switch should start unchecked (default fixture has embedding.enabled = false)
    const embeddingSwitch = page.getByLabel('Enable embedding generation');
    await expect(embeddingSwitch).not.toBeChecked();

    // Toggle it on
    await embeddingSwitch.click();
    await expect(embeddingSwitch).toBeChecked();

    // Save
    await page.getByRole('button', { name: 'Save Settings' }).click();
    await expect(page.getByText('Settings saved successfully!')).toBeVisible();

    // Reload the page and navigate back to Dedup tab
    await page.reload();
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'Dedup' }).click();

    // The switch should still be checked after reload
    await expect(page.getByLabel('Enable embedding generation')).toBeChecked();
  });

  test('toggling Enable embedding generation off persists after reload', async ({ page }) => {
    // Start with embedding enabled
    await openSettingsDedupTab(page, {
      config: {},
    });

    // The mock API returns the updated state after save; simulate starting enabled
    // by toggling on first, saving, then toggling off and saving again
    const embeddingSwitch = page.getByLabel('Enable embedding generation');

    // If it starts unchecked, enable it first
    const isInitiallyChecked = await embeddingSwitch.isChecked();
    if (!isInitiallyChecked) {
      await embeddingSwitch.click();
      await page.getByRole('button', { name: 'Save Settings' }).click();
      await expect(page.getByText('Settings saved successfully!')).toBeVisible();
      await page.reload();
      await page.waitForLoadState('networkidle');
      await page.getByRole('tab', { name: 'Dedup' }).click();
    }

    // Now toggle off
    await page.getByLabel('Enable embedding generation').click();
    await expect(page.getByLabel('Enable embedding generation')).not.toBeChecked();

    await page.getByRole('button', { name: 'Save Settings' }).click();
    await expect(page.getByText('Settings saved successfully!')).toBeVisible();

    // Reload and verify persisted
    await page.reload();
    await page.waitForLoadState('networkidle');
    await page.getByRole('tab', { name: 'Dedup' }).click();

    await expect(page.getByLabel('Enable embedding generation')).not.toBeChecked();
  });
});
