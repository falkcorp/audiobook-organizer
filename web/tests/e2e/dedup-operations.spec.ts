// file: web/tests/e2e/dedup-operations.spec.ts
// version: 1.2.0
// guid: e2f3a4b5-c6d7-8e9f-0a1b-2c3d4e5f6a7b
// last-edited: 2026-06-23

import { test, expect, type Page } from '@playwright/test';
import {
  setupMockApi,
  generateTestBooks,
  type MockAuthorDedupGroup,
} from './utils/test-helpers';

// ── Fixtures ──

const productionCompanyGroups: MockAuthorDedupGroup[] = [
  {
    canonical: { id: 20, name: 'Graphic Audio' },
    variants: [],
    book_count: 5,
    is_production_company: true,
  },
  {
    canonical: { id: 21, name: 'Soundbooth Theater' },
    variants: [],
    book_count: 3,
    is_production_company: true,
  },
];

const mixedGroups: MockAuthorDedupGroup[] = [
  {
    canonical: { id: 1, name: 'Brandon Sanderson' },
    variants: [{ id: 2, name: 'B. Sanderson' }],
    book_count: 10,
  },
  ...productionCompanyGroups,
  {
    canonical: { id: 30, name: 'Author A / Author B' },
    variants: [],
    book_count: 2,
    split_names: ['Author A', 'Author B'],
  },
];

async function setupDedupWithOperations(page: Page, opts: {
  groups?: MockAuthorDedupGroup[];
  activeOps?: Array<Record<string, unknown>>;
} = {}) {
  await setupMockApi(page, {
    books: generateTestBooks(5),
    authorDedup: { groups: opts.groups ?? mixedGroups },
    operations: {
      active: opts.activeOps || [],
      history: [],
      logs: {},
    },
  });

  // Mock the author books endpoint
  await page.route('**/api/v1/audiobooks?author_id=*', async (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        items: [
          { id: 'b1', title: 'The Stormlight Archive', author_name: 'Graphic Audio' },
          { id: 'b2', title: 'Warbreaker', author_name: 'Graphic Audio' },
        ],
        count: 2,
      }),
    });
  });
}

// ── Production Company Resolution Flow ──

test.describe('Production Company Resolution', () => {
  test('Find Real Author button only appears on production company groups', async ({ page }) => {
    await setupDedupWithOperations(page);
    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    const findBtns = page.getByRole('button', { name: /find real author/i });
    expect(await findBtns.count()).toBe(2);

    await expect(page.getByRole('button', { name: /merge into "brandon sanderson"/i })).toBeVisible();
  });

  test('production company badge is distinguishable from other groups', async ({ page }) => {
    await setupDedupWithOperations(page);
    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    const prodChips = page.getByText('Production Company');
    expect(await prodChips.count()).toBe(2);

    // Split group should show split names
    await expect(page.getByText('Author A').first()).toBeVisible();
    await expect(page.getByText('Author B').first()).toBeVisible();
  });

  test('clicking Find Real Author calls resolve API', async ({ page }) => {
    await setupDedupWithOperations(page);

    // Mock to return completed immediately
    let resolveCallCount = 0;
    await page.route('**/api/v1/authors/*/resolve-production', async (route) => {
      resolveCallCount++;
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          operation: { id: 'resolve-prod-1', type: 'resolve-production-author', status: 'completed', progress: 100, total: 100, message: 'Done' },
        }),
      });
    });
    await page.route('**/api/v1/operations/*/status', async (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'resolve-prod-1', status: 'completed', progress: 100, total: 100, message: 'Done' }),
      });
    });

    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    await page.getByRole('button', { name: /find real author/i }).first().click();
    // Wait for the API request rather than sleeping — avoids flakiness under load.
    await page.waitForRequest(req => req.url().includes('/resolve-production'));
    expect(resolveCallCount).toBeGreaterThan(0);
  });
});

// ── Operation Progress Integration ──

test.describe('Dedup Operation Progress', () => {
  test('merge operation shows success feedback', async ({ page }) => {
    await setupDedupWithOperations(page);
    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    await page.getByRole('button', { name: /merge into "brandon sanderson"/i }).click();

    // Should show success feedback
    await expect(page.getByText(/merged|success/i).first()).toBeVisible({ timeout: 5000 });
  });

  test('split operation shows feedback', async ({ page }) => {
    await setupDedupWithOperations(page);
    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    const splitBtn = page.getByRole('button', { name: /split into 2 authors/i });
    await expect(splitBtn).toBeVisible();

    await splitBtn.click();

    await expect(page.getByText(/split|updated|success/i).first()).toBeVisible({ timeout: 5000 });
  });
});

// ── Scheduler Task Integration ──

test.describe('Scheduler Tasks for Dedup', () => {
  test('system tasks endpoint returns dedup tasks', async ({ page }) => {
    await setupDedupWithOperations(page);

    await page.goto('/operations');
    await page.waitForLoadState('networkidle');

    const response = await page.evaluate(async () => {
      const resp = await fetch('/api/v1/system/tasks');
      return resp.json();
    });

    expect(response.tasks).toBeDefined();
    const taskNames = response.tasks.map((t: { name: string }) => t.name);
    expect(taskNames).toContain('dedup_refresh');
    expect(taskNames).toContain('resolve_production_authors');
  });

  test('manual task trigger creates operation', async ({ page }) => {
    await setupDedupWithOperations(page);
    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    const response = await page.evaluate(async () => {
      const resp = await fetch('/api/v1/system/tasks/dedup_refresh/run', { method: 'POST' });
      return resp.json();
    });

    expect(response.id).toBe('task-run-1');
    expect(response.status).toBe('running');
  });
});

// ── Error Handling ──

test.describe('Dedup Error Handling', () => {
  test('handles API error when fetching author duplicates', async ({ page }) => {
    await setupMockApi(page, {
      books: generateTestBooks(3),
    });

    // Override with error
    await page.route('**/api/v1/authors/duplicates', async (route) => {
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Internal server error' }),
      });
    });

    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    await expect(page.getByText(/error|failed/i).first()).toBeVisible();
  });

  test('handles merge failure gracefully', async ({ page }) => {
    await setupDedupWithOperations(page);
    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    // Override merge to fail
    await page.route('**/api/v1/authors/merge', async (route) => {
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Merge conflict' }),
      });
    });

    await page.getByRole('button', { name: /merge into "brandon sanderson"/i }).click();

    await expect(page.getByText(/error|failed|conflict/i).first()).toBeVisible();
  });

  test('handles resolve-production failure gracefully', async ({ page }) => {
    await setupDedupWithOperations(page);

    // Override resolve endpoint to fail
    await page.route('**/api/v1/authors/*/resolve-production', async (route) => {
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Resolution failed' }),
      });
    });

    await page.goto('/dedup?tab=authors');
    await page.waitForLoadState('networkidle');

    await page.getByRole('button', { name: /find real author/i }).first().click();

    await expect(page.getByText(/error|failed/i).first()).toBeVisible();
  });
});
