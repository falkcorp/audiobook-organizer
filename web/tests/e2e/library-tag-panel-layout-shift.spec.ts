// file: web/tests/e2e/library-tag-panel-layout-shift.spec.ts
// version: 2.1.0
// guid: 6b2f8c4e-1d7a-4f93-a5c0-9e3d7b1a2c58
// last-edited: 2026-10-21

import { test, expect, type Page } from '@playwright/test';
import { generateTestBooks, setupLibraryWithBooks } from './utils/test-helpers';

/**
 * The Browse-by-Tag panel used to mount only after the facets response landed,
 * pushing the Select All bar down mid-click. The panel now stays mounted with a
 * fixed-height body, so Select All must not move when facets arrive, whatever
 * the viewport width, tag length, expanded state or answer. jsdom has no
 * layout, so every case here is measured in a real browser.
 */

type Tag = { tag: string; count: number };

const SHORT: Tag[] = [
  { tag: 'fantasy', count: 8 },
  { tag: 'epic', count: 4 },
  { tag: 'scifi', count: 2 },
];

/** Twelve genre-length tags: wide enough to wrap at every width under test. */
const GENRES: Tag[] = [
  { tag: 'Science Fiction & Fantasy', count: 1840 },
  { tag: 'Epic Fantasy', count: 912 },
  { tag: 'metadata:language:en', count: 3001 },
  { tag: 'Military Science Fiction', count: 233 },
  { tag: 'LitRPG', count: 140 },
  { tag: 'Space Opera', count: 400 },
  { tag: 'Thriller', count: 77 },
  { tag: 'Mystery', count: 66 },
  { tag: 'Horror', count: 40 },
  { tag: 'Romance', count: 12 },
  { tag: 'Humor', count: 9 },
  { tag: 'Biography', count: 5 },
];

const HIDDEN_ONLY: Tag[] = [
  { tag: 'dedup:duration-match', count: 10 },
  { tag: 'metadata:source:audible', count: 10 },
];

/**
 * Serve the facets route in phases. Every request that arrives while a phase
 * is armed waits for `release()` and then answers that phase's tags. This is
 * phase-based, not per-request, because the Library can issue more than one
 * facets request while it settles (a superseded one is aborted), and all of
 * them belong to the same phase.
 */
async function gateFacets(page: Page, firstAnswer: Tag[]) {
  let phase = newPhase(firstAnswer);
  await page.route('**/api/v1/audiobooks/facets*', async (route) => {
    const mine = phase;
    await mine.promise;
    try {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ scoped_tags: mine.tags, scoped_total: 10 }),
      });
    } catch {
      // The client aborts a superseded request; fulfilling it then throws.
    }
  });
  return {
    /** Open the current phase: its held requests answer now. */
    release: () => phase.open(),
    /** Start a new held phase that will answer `tags`. */
    hold: (tags: Tag[]) => {
      phase = newPhase(tags);
    },
  };
}

function newPhase(tags: Tag[]) {
  let open!: () => void;
  const promise = new Promise<void>((resolve) => {
    open = resolve;
  });
  return { tags, promise, open };
}

async function open(page: Page, width: number, expanded: boolean) {
  await page.setViewportSize({ width, height: 900 });
  await setupLibraryWithBooks(page, generateTestBooks(10));
  if (expanded) {
    await page.addInitScript(() => localStorage.setItem('library-tag-cloud-expanded', 'true'));
  }
}

async function geometry(page: Page) {
  const selectAll = await page.getByLabel('Select All').boundingBox();
  const panel = await page.getByTestId('tag-cloud-panel').boundingBox();
  expect(selectAll, 'Select All must be laid out').not.toBeNull();
  expect(panel, 'tag panel must stay mounted').not.toBeNull();
  return { selectAllY: selectAll!.y, panelHeight: panel!.height };
}

const WIDTHS = [1280, 1024, 768, 390];

test.describe('library tag panel layout stability', () => {
  for (const width of WIDTHS) {
    for (const [name, tags] of [
      ['short tags', SHORT],
      ['12 genre-length tags', GENRES],
    ] as const) {
      test(`${name} at ${width}px: Select All does not move`, async ({ page }) => {
        await open(page, width, false);
        const facets = await gateFacets(page, tags);
        await page.goto('/library');
        await expect(page.getByTestId('tag-cloud-skeleton').first()).toBeVisible();
        await expect(page.getByLabel('Select All')).toBeVisible();
        const before = await geometry(page);

        facets.release();
        await expect(page.getByTestId('tag-cloud-skeleton')).toHaveCount(0);
        await expect(page.getByTestId('tag-cloud-body')).toBeVisible();

        expect(await geometry(page)).toEqual(before);
      });
    }
  }

  for (const width of [1280, 390]) {
    test(`expanded panel with 12 genre-length tags at ${width}px: Select All does not move`, async ({
      page,
    }) => {
      await open(page, width, true);
      const facets = await gateFacets(page, GENRES);
      await page.goto('/library');
      await expect(page.getByTestId('tag-cloud-skeleton').first()).toBeVisible();
      await expect(page.getByLabel('Select All')).toBeVisible();
      const before = await geometry(page);

      facets.release();
      await expect(page.getByTestId('tag-cloud-skeleton')).toHaveCount(0);
      await expect(page.getByText('Epic Fantasy (912)')).toBeVisible();

      expect(await geometry(page)).toEqual(before);
    });
  }

  test('an answer made only of hidden-namespace tags keeps the panel mounted', async ({ page }) => {
    await open(page, 1280, false);
    const facets = await gateFacets(page, HIDDEN_ONLY);
    await page.goto('/library');
    await expect(page.getByTestId('tag-cloud-skeleton').first()).toBeVisible();
    await expect(page.getByLabel('Select All')).toBeVisible();
    const before = await geometry(page);

    facets.release();
    await expect(page.getByTestId('tag-cloud-skeleton')).toHaveCount(0);
    await expect(page.getByText('No tags in these results.')).toBeVisible();

    expect(await geometry(page)).toEqual(before);
  });

  for (const width of [1280, 390]) {
    test(`a selected long low-count tag keeps "Show all" inside the row at ${width}px`, async ({
      page,
    }) => {
      // The selected tag is prioritised into the collapsed row ahead of the
      // busier ones, so the fit count must be measured in that same order. If
      // it is measured in count order instead, the long chip is clipped and
      // "Show all N" ends up outside the row while still focusable.
      const LONG = 'An Exceptionally Long Low Count Genre Name';
      const tags: Tag[] = [...GENRES.slice(0, 8), { tag: LONG, count: 1 }];
      await open(page, width, true); // saved expanded, so we exercise collapse
      const facets = await gateFacets(page, tags);
      await page.goto(`/library?search=${encodeURIComponent(`tag:"${LONG}"`)}`);
      facets.release();
      await expect(page.getByTestId('tag-cloud-panel')).toHaveAttribute('aria-busy', 'false');
      await page.getByRole('button', { name: 'Collapse tag cloud' }).click();

      const body = page.getByTestId('tag-cloud-body');
      const showAll = page.getByRole('button', { name: 'Show all 9' });
      await expect(showAll).toBeVisible();

      const row = (await body.boundingBox())!;
      const rowRight = row.x + row.width;
      const children = await body.locator(':scope > *').all();
      expect(children.length).toBeGreaterThan(0);
      for (const child of children) {
        const box = (await child.boundingBox())!;
        expect(box.x + box.width, await child.innerText()).toBeLessThanOrEqual(rowRight + 0.5);
      }
      const buttonBox = (await showAll.boundingBox())!;
      expect(buttonBox.x + buttonBox.width).toBeLessThanOrEqual(rowRight + 0.5);
    });
  }

  test('geometry is stable across a filter change (reload, then new answer)', async ({ page }) => {
    await open(page, 1024, false);
    const facets = await gateFacets(page, GENRES);
    await page.goto('/library');
    await expect(page.getByLabel('Select All')).toBeVisible();
    const skeleton = await geometry(page);

    facets.release();
    await expect(page.getByText('language: en (3001)')).toBeVisible();
    const loaded = await geometry(page);
    expect(loaded).toEqual(skeleton);

    // A chip click appends tag:"x" to the search, which changes the predicate
    // and fires a second facets request that we hold back. The click itself
    // adds the search box's own active-filter chip row ABOVE the panel, so
    // Select All's absolute y legitimately moves at click time; that is the
    // user's action, not the facets answer. What this PR owns is: the panel
    // keeps its height through the reload, and nothing moves when the new
    // answer lands.
    facets.hold(SHORT);
    await page.getByText('language: en (3001)').click();
    await expect(page.getByTestId('tag-cloud-spinner')).toBeVisible();
    const reloading = await geometry(page);
    expect(reloading.panelHeight).toBe(loaded.panelHeight);

    facets.release();
    await expect(page.getByText('fantasy (8)')).toBeVisible();
    await expect(page.getByTestId('tag-cloud-spinner')).toHaveCount(0);
    expect(await geometry(page)).toEqual(reloading);
  });
});
