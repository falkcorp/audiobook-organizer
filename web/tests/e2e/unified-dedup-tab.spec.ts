// file: web/tests/e2e/unified-dedup-tab.spec.ts
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1234-efab-555678901234
// last-edited: 2026-08-09

// Playwright E2E flow for the unified dedup tab:
//   1. Enable feature flag via localStorage
//   2. Navigate to /dedup
//   3. Unified tab surface renders (band filter bar visible)
//   4. Click CERTAIN band chip — table re-queries
//   5. Click the info button on a candidate row — breakdown drawer opens
//   6. Switch to "Score Breakdown" tab inside drawer — breakdown renders

import { test, expect, type Page } from '@playwright/test';
import { setupPhase2Interactive } from './utils/test-helpers';

const MOCK_CANDIDATE = {
  id: 42,
  entity_type: 'book',
  entity_a_id: '01ABCDEFGHIJKLMNOPQRSTUV01',
  entity_b_id: '01ABCDEFGHIJKLMNOPQRSTUV02',
  layer: 'embedding',
  similarity: 0.95,
  status: 'pending',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  // UnifiedDedupTab fetches candidates with include_books=true and renders each
  // side from candidate.book_a / candidate.book_b — there is no per-book
  // getBook() fan-out. Without these the rows render "(missing book — …)"
  // instead of a title, which is what every assertion here used to trip on.
  book_a: {
    id: '01ABCDEFGHIJKLMNOPQRSTUV01',
    title: 'Foundation',
    author_name: 'Isaac Asimov',
    file_path: '/mnt/bigdata/books/audiobook-organizer/Foundation/Foundation.mp3',
    asin: 'B002V0QC4Q',
    isbn13: '9780553293357',
    cover_url: '',
    narrator: 'Scott Brick',
  },
  book_b: {
    id: '01ABCDEFGHIJKLMNOPQRSTUV02',
    title: 'Foundation (Duplicate)',
    author_name: 'Isaac Asimov',
    file_path: '/mnt/bigdata/books/audiobook-organizer/Foundation2/Foundation.m4b',
  },
  band: 'CERTAIN',
  score: 98.0,
  score_breakdown: {
    score: 98.0,
    band: 'CERTAIN',
    formula: 'v2',
    signals: [
      {
        kind: 'exact_file',
        value: 1.0,
        weight: 100,
        evidence: 'Exact file hash match',
        primary: true,
      },
      {
        kind: 'embedding_high',
        value: 0.97,
        weight: 80,
        evidence: 'High embedding similarity',
        primary: true,
      },
    ],
  },
};

const MOCK_STATS = {
  stats: [
    { entity_type: 'book', layer: 'embedding', status: 'pending', count: 5 },
    { entity_type: 'book', layer: 'exact', status: 'pending', count: 2 },
  ],
};

const MOCK_BREAKDOWN = {
  candidate: MOCK_CANDIDATE,
  book_a: {
    id: MOCK_CANDIDATE.entity_a_id,
    title: 'Foundation',
    author_name: 'Isaac Asimov',
    files: [
      {
        id: 'file-01',
        file_path: '/mnt/bigdata/books/audiobook-organizer/Foundation/Foundation.mp3',
        format: 'mp3',
        bitrate: 128,
        file_size: 52000000,
        duration: 3600,
      },
    ],
  },
  book_b: {
    id: MOCK_CANDIDATE.entity_b_id,
    title: 'Foundation (Duplicate)',
    author_name: 'Isaac Asimov',
    files: [
      {
        id: 'file-02',
        file_path: '/mnt/bigdata/books/audiobook-organizer/Foundation2/Foundation.m4b',
        format: 'm4b',
        bitrate: 64,
        file_size: 30000000,
        duration: 3595,
      },
    ],
  },
};

// The candidate row identifies its books by a title link to /library/<id>.
// Matching on the href keeps the assertion pinned to this exact candidate
// without depending on the raw ULID being rendered as text (it is not — the
// UI only shows the last 8 characters, and then only when the book is missing).
function bookALink(page: Page) {
  return page.locator(`a[href="/library/${MOCK_CANDIDATE.entity_a_id}"]`).first();
}

// The per-row info IconButton (aria-label="Open comparison for candidate <id>")
// was replaced by a labelled "Compare" Button with no candidate-specific label,
// so scope the lookup to the row that holds Book A's link.
function compareButton(page: Page) {
  return page
    .getByRole('row')
    .filter({ has: page.locator(`a[href="/library/${MOCK_CANDIDATE.entity_a_id}"]`) })
    .getByRole('button', { name: /compare/i });
}

async function enableUnifiedDedupFeatureFlag(page: Page) {
  // Set the feature flag via localStorage before the app JS runs.
  await page.addInitScript(() => {
    localStorage.setItem('feature_unified_dedup', '1');
  });
}

async function mockDedupRoutes(page: Page) {
  // Stats endpoint.
  await page.route('**/api/v1/dedup/stats', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: MOCK_STATS }),
    });
  });

  // Candidates endpoint — handle with/without band param.
  await page.route('**/api/v1/dedup/candidates**', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: { candidates: [MOCK_CANDIDATE], total: 1 },
      }),
    });
  });

  // Breakdown endpoint.
  await page.route('**/api/v1/dedup/candidates/42/breakdown', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: MOCK_BREAKDOWN }),
    });
  });

  // Rescore endpoint (not exercised in this flow but prevents 404 noise).
  await page.route('**/api/v1/dedup/rescore', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: { inspected: 7, skipped: 0, changed: 1, applied: true, band_deltas: {} },
      }),
    });
  });

  // Scan endpoint.
  await page.route('**/api/v1/dedup/scan', (route) => {
    route.fulfill({
      status: 202,
      contentType: 'application/json',
      body: JSON.stringify({ data: { op_id: 'op-test-123', type: 'dedup.full-scan' } }),
    });
  });
}

test.describe('Unified Dedup Tab (T017)', () => {
  test('unified tab renders band filter bar and candidate table', async ({ page }) => {
    await enableUnifiedDedupFeatureFlag(page);
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);

    await page.goto('/dedup');
    await page.waitForLoadState('domcontentloaded');

    // The unified tab wrapper should be visible (feature flag enabled, legacy not toggled).
    await expect(page.locator('[data-testid="unified-dedup-tab-wrapper"]')).toBeVisible();

    // Band filter bar should render.
    await expect(page.locator('[data-testid="band-filter-bar"]')).toBeVisible();
    await expect(page.locator('[data-testid="band-chip-CERTAIN"]')).toBeVisible();
    await expect(page.locator('[data-testid="band-chip-HIGH"]')).toBeVisible();
    await expect(page.locator('[data-testid="band-chip-MEDIUM"]')).toBeVisible();
    await expect(page.locator('[data-testid="band-chip-REVIEW"]')).toBeVisible();
  });

  test('filtering by CERTAIN band updates the table', async ({ page }) => {
    await enableUnifiedDedupFeatureFlag(page);
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);

    await page.goto('/dedup');
    await page.waitForLoadState('domcontentloaded');

    // Wait for the band filter to be ready.
    await expect(page.locator('[data-testid="band-chip-CERTAIN"]')).toBeVisible();

    // Click CERTAIN band — should set band param in URL and re-fetch.
    await page.locator('[data-testid="band-chip-CERTAIN"]').click();
    await expect(page).toHaveURL(/band=CERTAIN/);

    // Candidate row should still be visible (mock returns same data).
    await expect(bookALink(page)).toBeVisible();
  });

  test('clicking info button opens comparison drawer', async ({ page }) => {
    await enableUnifiedDedupFeatureFlag(page);
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);

    await page.goto('/dedup');
    await page.waitForLoadState('domcontentloaded');

    // Wait for table to populate.
    await expect(bookALink(page)).toBeVisible({ timeout: 10000 });

    // Click the info icon button for the first candidate row.
    await compareButton(page).click();

    // Drawer should open.
    await expect(page.locator('[data-testid="candidate-compare-drawer"]')).toBeVisible();
    await expect(page.locator('text=Candidate #42')).toBeVisible();
  });

  test('score breakdown renders in drawer', async ({ page }) => {
    await enableUnifiedDedupFeatureFlag(page);
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);

    await page.goto('/dedup');
    await page.waitForLoadState('domcontentloaded');

    await expect(bookALink(page)).toBeVisible({ timeout: 10000 });
    await compareButton(page).click();
    await expect(page.locator('[data-testid="candidate-compare-drawer"]')).toBeVisible();

    // Switch to "Score Breakdown" tab inside the drawer.
    await page.locator('[data-testid="drawer-tab-breakdown"]').click();

    // Breakdown panel should render with signal data.
    await expect(page.locator('[data-testid="score-breakdown-panel"]')).toBeVisible();
    await expect(page.locator('[data-testid="score-stacked-bar"]')).toBeVisible();
    await expect(page.locator('text=Exact file hash')).toBeVisible();
  });

  test('legacy toggle shows legacy tab view', async ({ page }) => {
    await enableUnifiedDedupFeatureFlag(page);
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);

    await page.goto('/dedup');
    await page.waitForLoadState('domcontentloaded');

    // Feature is enabled, so unified should be visible.
    await expect(page.locator('[data-testid="unified-dedup-tab-wrapper"]')).toBeVisible();

    // Click legacy toggle.
    await page.locator('[data-testid="legacy-toggle-btn"]').click();

    // Unified tab should be hidden; legacy tab bar should appear.
    await expect(page.locator('[data-testid="unified-dedup-tab-wrapper"]')).not.toBeVisible();
    await expect(page.locator('text=Version Groups')).toBeVisible();

    // Toggle back to new view.
    await page.locator('[data-testid="legacy-toggle-btn"]').click();
    await expect(page.locator('[data-testid="unified-dedup-tab-wrapper"]')).toBeVisible();
  });
});
