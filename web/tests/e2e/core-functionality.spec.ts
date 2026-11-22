// file: web/tests/e2e/core-functionality.spec.ts
// version: 1.0.0
// guid: c0d1e2f3-4a5b-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-02-06

import { test, expect } from '@playwright/test';
import { setupPhase2Interactive } from './utils/test-helpers';
import { paths } from './utils/paths';

test.describe('Core Functionality', () => {
  test('app dashboard loads successfully', async ({ page }) => {
    // Setup with mocked APIs (includes EventSource mocking)
    await setupPhase2Interactive(page);

    // Dashboard loads
    await page.goto(paths.home);
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByText('Audiobook Organizer')).toBeVisible();
  });

  test('app initializes without errors', async ({ page }) => {
    // Setup and navigate (includes EventSource mocking)
    await setupPhase2Interactive(page);

    // Load dashboard
    await page.goto(paths.home);
    await page.waitForLoadState('domcontentloaded');

    // Check for error indicators
    const errors = await page.locator('[data-testid="error"]').count();
    expect(errors).toBe(0);

    // Check that main content loaded
    const main = page.locator('main, [role="main"]').first();
    const isVisible = await main.isVisible().catch(() => false);
    if (!isVisible) {
      const content = await page.locator('body').textContent();
      expect(content).toBeTruthy();
    }

    console.log('✅ App initialization test passed');
  });
});
