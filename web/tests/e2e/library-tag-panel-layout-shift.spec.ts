// file: web/tests/e2e/library-tag-panel-layout-shift.spec.ts
// version: 1.0.0
// guid: 6b2f8c4e-1d7a-4f93-a5c0-9e3d7b1a2c58
// last-edited: 2026-10-21

import { test, expect } from '@playwright/test';
import { generateTestBooks, setupLibraryWithBooks } from './utils/test-helpers';

/**
 * The Browse-by-Tag panel used to mount only after the facets response landed,
 * pushing the Select All bar down ~84px mid-click. The panel now stays mounted
 * with a reserved-height body, so Select All must not move when facets arrive.
 * jsdom has no layout, so this is measured in a real browser.
 */
test.describe('library tag panel layout stability', () => {
  test('Select All does not move when the facets response arrives', async ({ page }) => {
    await setupLibraryWithBooks(page, generateTestBooks(10));

    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    // Registered after the shared mock, so it takes precedence for facets.
    await page.route('**/api/v1/audiobooks/facets*', async (route) => {
      await gate;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          scoped_tags: [
            { tag: 'fantasy', count: 8 },
            { tag: 'epic', count: 4 },
            { tag: 'scifi', count: 2 },
          ],
          scoped_total: 10,
        }),
      });
    });

    await page.goto('/library');
    const panel = page.getByTestId('tag-cloud-panel');
    const selectAll = page.getByLabel('Select All');
    await expect(panel).toBeVisible();
    await expect(page.getByTestId('tag-cloud-skeleton').first()).toBeVisible();
    await expect(selectAll).toBeVisible();

    const before = await selectAll.boundingBox();
    const panelBefore = await panel.boundingBox();
    expect(before).not.toBeNull();

    release();
    await expect(page.getByText('fantasy (8)')).toBeVisible();

    const after = await selectAll.boundingBox();
    const panelAfter = await panel.boundingBox();
    expect(after).toEqual(before);
    expect(panelAfter?.height).toBe(panelBefore?.height);
  });
});
