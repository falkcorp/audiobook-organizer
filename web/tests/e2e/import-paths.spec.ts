// file: tests/e2e/import-paths.spec.ts
// version: 1.2.0
// guid: e3f4a5b6-c7d8-9e0f-1a2b-3c4d5e6f7a8b
// last-edited: 2026-08-09

import { test, expect } from '@playwright/test';
import { setupMockApi } from './utils/test-helpers';

test.describe('Import paths workflows', () => {
  // Setup handled per-test; setupMockApi provides base routes

  test('add and remove import path via Settings page (mocked API)', async ({
    page,
  }) => {
    let importPaths: Array<{
      id: number;
      path: string;
      name: string;
      enabled: boolean;
      created_at: string;
      book_count: number;
    }> = [];
    let nextId = 1;

    // Set up base mock routes (config, audiobooks, etc.)
    await setupMockApi(page);

    // Override import-paths with test-specific stateful mock
    await page.route('**/api/v1/import-paths', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          // api.getImportPaths reads body.data.importPaths (api.ts:1764), so an
          // un-enveloped body made body.data undefined and the call threw —
          // the list stayed empty however many paths had been added.
          body: JSON.stringify({ importPaths, data: { importPaths } }),
        });
      }
      if (route.request().method() === 'POST') {
        const body = route.request().postDataJSON() as {
          path: string;
          name: string;
        };
        const created = {
          id: nextId++,
          path: body.path,
          name: body.name,
          enabled: true,
          created_at: new Date().toISOString(),
          book_count: 0,
        };
        importPaths.push(created);
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          // api.addImportPath reads body.data.importPath (api.ts:1779).
          body: JSON.stringify({ importPath: created, data: { importPath: created } }),
        });
      }
      if (route.request().method() === 'DELETE') {
        const idStr = route.request().url().split('/').pop() || '';
        const id = Number(idStr);
        importPaths = importPaths.filter((p) => p.id !== id);
        return route.fulfill({ status: 200 });
      }
      return route.fulfill({ status: 200, body: '{}' });
    });

    await page.route('**/api/v1/system/status', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        // api.getSystemStatus returns body.data (api.ts:2070).
        body: JSON.stringify({
          data: {
            status: 'ok',
            library: { book_count: 0, folder_count: 1, total_size: 0 },
            import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
            memory: {},
            runtime: {},
            operations: { recent: [] },
          },
        }),
      });
    });

    await page.route('**/api/v1/health', (route) => {
      route.fulfill({ status: 200, body: JSON.stringify({ status: 'ok' }) });
    });

    await page.goto('/settings');

    await expect(
      page.getByText('Settings', { exact: true }).first()
    ).toBeVisible();

    // Add import path
    // Import-path management lives on the Settings "Paths" tab now, not the
    // Library tab this test used to land on.
    await page.getByRole('tab', { name: 'Paths' }).click();
    await page.getByRole('button', { name: 'Add Import Path' }).click();
    const dialog = page.getByRole('dialog', { name: /Add Import Path/i });
    const pathInput = dialog.getByPlaceholder('/path/to/downloads');
    await pathInput.fill('/tmp/books');
    const saveButton = dialog
      .getByRole('button', { name: /Add|Save/i })
      .first();
    await saveButton.click();

    await expect(page.getByText('/tmp/books')).toBeVisible();
    await expect(page.getByText('Tmp')).toBeVisible();

    // Remove import path via DELETE API call (route mock updates state)
    await page.evaluate(() =>
      fetch('/api/v1/import-paths/1', { method: 'DELETE' })
    );
    await page.reload();
    await expect(page.getByText('/tmp/books')).not.toBeVisible({
      timeout: 5000,
    });
  });
});
