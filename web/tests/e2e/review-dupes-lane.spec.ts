// file: web/tests/e2e/review-dupes-lane.spec.ts
// version: 2.0.0
// guid: 47849aa3-3814-42c5-81aa-299a39fb5384
// last-edited: 2026-08-20

// End-to-end coverage for the dupes lane of /review.
//
// This spec used to drive UnifiedDedupTab at /dedup, which Phase 7 deleted. It
// was rewritten rather than removed: /review had NO e2e coverage at all, so
// deleting the suite that covered the surface it replaces would have traded
// five passing tests for a blind spot on the screen this whole project built.
//
// The mocks survived the move untouched -- both surfaces call the same
// /api/v1/dedup/* endpoints, and the comparison drawer is literally the same
// component. What changed is the route, the wrapper test id, and the removal of
// a feature flag that no longer exists.
//
// The one test genuinely deleted is the legacy-view toggle, whose subject
// (sessionStorage.dedup_show_legacy) is gone. In its place: a deep-link test,
// covering the defect where /review?book=<id> opened the metadata lane and left
// the dupes lane's server-side filter unreachable.

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
  // Scoped by the row's own test id rather than by table semantics -- the lane
  // renders cards in the two-column and auto view modes, where there is no row.
  return page
    .locator(`[data-testid="dupes-row-${MOCK_CANDIDATE.id}"]`)
    .getByRole('button', { name: /compare/i });
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

test.describe('the dupes lane of /review', () => {
  test('renders the rail, the band filter and the candidate table', async ({ page }) => {
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);

    await page.goto('/review?lane=dupes');
    await page.waitForLoadState('domcontentloaded');

    await expect(page.locator('[data-testid="dupes-rail"]')).toBeVisible();

    // The band filter, asserted through its chips. UnifiedDedupTab wrapped these
    // in a `band-filter-bar` element; the lane puts them straight in the rail,
    // which the assertion above already covers -- so the chips ARE the filter.
    await expect(page.locator('[data-testid="band-chip-CERTAIN"]')).toBeVisible();
    await expect(page.locator('[data-testid="band-chip-HIGH"]')).toBeVisible();
    await expect(page.locator('[data-testid="band-chip-MEDIUM"]')).toBeVisible();
    await expect(page.locator('[data-testid="band-chip-REVIEW"]')).toBeVisible();
  });

  test('filtering by CERTAIN band updates the table', async ({ page }) => {
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);

    await page.goto('/review?lane=dupes');
    await page.waitForLoadState('domcontentloaded');

    // Wait for the band filter to be ready.
    await expect(page.locator('[data-testid="band-chip-CERTAIN"]')).toBeVisible();

    // Click CERTAIN band — should set band param in URL and re-fetch.
    await page.locator('[data-testid="band-chip-CERTAIN"]').click();
    await expect(page).toHaveURL(/band=CERTAIN/);

    // Candidate row should still be visible (mock returns same data).
    await expect(bookALink(page)).toBeVisible();
  });

  test('opens the comparison drawer from a candidate row', async ({ page }) => {
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);

    await page.goto('/review?lane=dupes');
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
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);

    await page.goto('/review?lane=dupes');
    await page.waitForLoadState('domcontentloaded');

    await expect(bookALink(page)).toBeVisible({ timeout: 10000 });
    await compareButton(page).click();
    await expect(page.locator('[data-testid="candidate-compare-drawer"]')).toBeVisible();

    // Switch to "Score Breakdown" tab inside the drawer.
    await page.locator('[data-testid="drawer-tab-breakdown"]').click();

    // Breakdown panel should render with signal data.
    //
    // ScoreBreakdownPanel was promoted to the shared EvidencePanel, which names
    // its test ids by EVIDENCE KIND rather than by lane: dedup's score is a
    // weighted sum, so it renders the `weighted` view -- the only one of the
    // three kinds entitled to a stacked share bar. See
    // src/components/review/evidence/types.ts.
    await expect(page.locator('[data-testid="evidence-weighted"]')).toBeVisible();
    await expect(page.locator('[data-testid="evidence-stacked-bar"]')).toBeVisible();
    await expect(page.locator('text=Exact file hash')).toBeVisible();
  });

  test('a ?book= deep link opens the dupes lane, filtered server-side', async ({ page }) => {
    // The defect this covers: /review opened the metadata lane regardless of the
    // URL, and because each lane only fetches while it is the visible one, the
    // dupes lane stayed inactive and never applied the entity filter. The link
    // worked, the filter worked, and the two could not reach each other.
    let entityFiltered = false;
    await setupPhase2Interactive(page);
    await mockDedupRoutes(page);
    await page.route('**/api/v1/dedup/candidates**', (route) => {
      if (route.request().url().includes(`entity_id=${MOCK_CANDIDATE.entity_a_id}`)) {
        entityFiltered = true;
      }
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: { candidates: [MOCK_CANDIDATE], total: 1 } }),
      });
    });

    await page.goto(`/review?book=${MOCK_CANDIDATE.entity_a_id}`);
    await page.waitForLoadState('domcontentloaded');

    // Arrived on dupes without anyone clicking a lane tab.
    await expect(page.locator('[data-testid="dupes-rail"]')).toBeVisible();
    await expect(page.locator('[data-testid="dupes-deeplink-banner"]')).toBeVisible();
    await expect.poll(() => entityFiltered).toBe(true);
  });
});
