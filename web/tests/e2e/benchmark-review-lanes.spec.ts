// file: web/tests/e2e/benchmark-review-lanes.spec.ts
// version: 1.1.0
// guid: e0d8440c-7578-4a92-9f69-4d05bae4b33e
// last-edited: 2026-09-01

import { test, expect, type Page, type Locator } from '@playwright/test';
import { setupPhase2Interactive } from './utils/test-helpers';

/**
 * MEASUREMENT harness for "is /review responsive at 50 and 100 items?".
 *
 * WHY THIS FILE EXISTS
 *
 * Nothing had ever measured it. `perf(review): memoize spine rows so one
 * checkbox re-renders one row` (d01f15a87, in main) states that at the 100-row
 * page cap a checkbox tick costs "99 wasted row renders per click, which is the
 * sluggishness the larger page sizes actually exhibit". That is a re-render
 * COUNT and an inference from it; no wall-clock number backed it. Same for
 * `perf(review): stop the 30s poll re-rendering the whole review route`
 * (4762b1784). This file produces the first real numbers.
 *
 * It is a measuring instrument, not a gate. Like benchmark-library-load.spec.ts
 * it is skipped unless E2E_PERF=1, it is excluded from the chromium/webkit gate
 * projects by the '**\/benchmark-*.spec.ts' testIgnore, and it is named in
 * GATE_EXEMPT in check-spec-discovery.mjs. It asserts almost nothing about time
 * on purpose: a wall-clock threshold on a shared runner is a flake factory, and
 * a threshold loose enough not to flake is loose enough to pass while the page
 * is slow. The output is a table you read, not a boolean.
 *
 *   E2E_PERF=1 npx playwright test -c tests/e2e/playwright.config.ts \
 *     --project=benchmark --workers=1 -g "review lane"
 *
 * ---------------------------------------------------------------------------
 * WHAT THIS MEASURES, AND — MORE IMPORTANTLY — WHAT IT DOES NOT
 * ---------------------------------------------------------------------------
 *
 * Every row here is seeded with Playwright `page.route` interception, not by
 * writing rows into the backend. That is a deliberate choice and it bounds the
 * claim:
 *
 *   MEASURED      client-side cost only — JSON parse, the lanes' filter/sort/
 *                 group passes, React reconciliation, MUI render, style/layout.
 *   NOT MEASURED  server latency, Pebble read cost, the real query planner,
 *                 payload size over a real socket, or ANY server-side filter.
 *
 * So a number here is a floor on what a user experiences, never the whole of
 * it. A lane that is slow here is slow for the user; a lane that is fast here
 * can still feel slow in production because of the half this harness stubs out.
 *
 * The interception was chosen over real seeding because the question is about
 * RENDER responsiveness at a given row count, and interception is the only way
 * to pin the row count exactly and identically across lanes. Real seeding would
 * measure a different (also worth measuring) thing and could not hold N fixed.
 *
 * CONSEQUENCE FOR "APPLY A FILTER". The three lanes do not filter alike, and
 * averaging them would be a lie:
 *
 *   dupes    band chips / status / "both unmatched" are SERVER-SIDE — they
 *            re-issue GET /dedup/candidates. Under interception, measuring one
 *            of those measures this file's own fulfill latency plus a re-render
 *            of the same N rows. It is NOT a filter-cost measurement, so it is
 *            not reported as one. The "Search this page" box IS client-side
 *            (and undebounced), so that is what is measured for this lane.
 *   metadata "Title filter" is client-side and undebounced. Measured.
 *            (There is no server-side filter pushdown on this lane at all —
 *            see the "metadata review filter-pushdown prerequisite" TODO.)
 *   regroup  "Search loaded holds" is client-side but DEBOUNCED 250 ms
 *            (REGROUP_SEARCH_DEBOUNCE_MS). The reported wall-clock therefore
 *            contains a 250 ms floor that is a deliberate product decision, not
 *            a cost. Read the longtask columns for this lane, not the ms.
 *            The "kind" select is server-side and is not measured.
 *
 * CONSEQUENCE FOR "TOGGLE A CHECKBOX" AND "CHANGE SORT". Two of the four
 * requested interactions do not exist on every lane. They are reported as
 * absent rather than synthesised:
 *
 *   checkbox   dupes ✓   metadata ✓   regroup ✗ — the regroup lane has NO row
 *              selection whatsoever. Rows carry per-row Approve/Reject inside a
 *              collapsed Accordion; bulk actions are per-bucket.
 *   sort       dupes ✗   metadata ✗   regroup ✓ — neither useDupesLane nor
 *              useMetadataLane has a sort field, and neither panel renders a
 *              sort control. Only regroup has one ("Sort holds"), and it is a
 *              pure client-side re-sort of already-loaded rows.
 *
 * ---------------------------------------------------------------------------
 * WHY WALL-CLOCK ALONE WOULD BE WORTHLESS HERE
 * ---------------------------------------------------------------------------
 *
 * `Date.now()` around a Playwright `click()` carries a floor of tens of ms from
 * actionability checks and the CDP round trip. That floor is plausibly LARGER
 * than the signal, so "38 ms at N=50, 41 ms at N=100" could be two readings of
 * the harness rather than of the app. Three things guard against reporting
 * noise as a finding:
 *
 *   1. Every interaction also reports longtask deltas — blockingMs (TBT, summed
 *      over-50ms time) and maxTaskMs (the longest single main-thread task).
 *      maxTaskMs is what a user actually feels; a sum can hide a 300 ms stall.
 *      These are measured INSIDE the page and do not include CDP latency.
 *   2. A NOISE FLOOR row at N=5 runs the identical code path. If the N=100 row
 *      is not clearly above the N=5 row, the comparison is not evidence.
 *   3. Each interaction is repeated REPS times inside one page load and the
 *      MEDIAN is reported with min/max, so one scheduling hiccup cannot become
 *      the headline number.
 *
 * The suite is serial and should be run with --workers=1: two workers measuring
 * wall-clock on one machine measure each other. Record the machine's load
 * average alongside any number taken from this file.
 *
 * ---------------------------------------------------------------------------
 * CONTROL EXPERIMENT (committed)
 * ---------------------------------------------------------------------------
 *
 * A measurement that does not respond to a known-bad input is not an
 * instrument. Two committed controls prove this one responds:
 *
 *   - CPU THROTTLE. The N=100 case is repeated at 6x CPU throttle via
 *     Emulation.setCPUThrottlingRate. If the throttled numbers do not rise well
 *     above the unthrottled ones, the harness is measuring its own overhead and
 *     every other row in the table is void.
 *   - N=500 on regroup, which is REGROUP_FETCH_LIMIT — the largest row count
 *     the lane can actually be made to render.
 *
 * Neither control touches production code, which is what keeps this PR
 * test-only.
 */

// --- tuning ----------------------------------------------------------------

/** Row counts under test. 50 and 100 are two of the three real page-size
 *  options the UI offers (PAGE_SIZE_OPTIONS = [25, 50, 100]), so these are
 *  sizes a user can actually select, not synthetic numbers. */
const SIZES = [50, 100] as const;

/** The noise-floor row. Small enough that per-row cost cannot dominate. */
const FLOOR_N = 5;

/** Repetitions per interaction, inside one page load. Median is reported. */
const REPS = 5;

const PAGE_SIZE_STORAGE_KEY = 'metadata-review-page-size';

// --- sample collection -----------------------------------------------------

interface Sample {
  lane: string;
  n: number;
  metric: string;
  /** Median wall-clock over REPS (or the single value for a load). */
  medianMs: number;
  minMs: number;
  maxMs: number;
  reps: number;
  /**
   * Longtask counters summed over the WHOLE batch of reps -- and over both
   * directions of each rep (apply AND clear, plus both settles), because
   * __perf is reset once per batch rather than once per timed window. So these
   * do NOT line up arithmetically with medianMs, and must not be read as "the
   * cost of one apply". They answer a coarser question, which is the one that
   * survives the CDP floor: did this lane block the main thread at all?
   */
  longTasks: number;
  blockingMs: number;
  maxTaskMs: number;
  note: string;
}

const results: Sample[] = [];

function median(xs: number[]): number {
  const s = [...xs].sort((a, b) => a - b);
  const mid = Math.floor(s.length / 2);
  return s.length % 2 ? s[mid] : Math.round((s[mid - 1] + s[mid]) / 2);
}

// --- in-page instrumentation ----------------------------------------------

interface PerfWindow extends Window {
  __perf: { count: number; blocking: number; max: number };
}

/**
 * Install a longtask observer before any app code runs, and optionally throttle
 * the CPU.
 *
 * 'longtask' is chromium-only; the benchmark project is Desktop Chrome, so this
 * is always supported there. The try/catch keeps a stray webkit run alive with
 * zeroed counters rather than failing the navigation.
 */
async function instrument(page: Page, cpuThrottle = 1): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as PerfWindow;
    w.__perf = { count: 0, blocking: 0, max: 0 };
    try {
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          w.__perf.count += 1;
          w.__perf.blocking += Math.max(0, entry.duration - 50);
          w.__perf.max = Math.max(w.__perf.max, entry.duration);
        }
      }).observe({ entryTypes: ['longtask'] });
    } catch {
      /* not supported on this engine — counters stay zero */
    }
  });
  if (cpuThrottle > 1) {
    const client = await page.context().newCDPSession(page);
    await client.send('Emulation.setCPUThrottlingRate', { rate: cpuThrottle });
  }
}

async function resetPerf(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as PerfWindow;
    w.__perf = { count: 0, blocking: 0, max: 0 };
  });
}

async function readPerf(page: Page) {
  return page.evaluate(() => {
    const w = window as unknown as PerfWindow;
    return {
      count: w.__perf?.count ?? 0,
      blocking: Math.round(w.__perf?.blocking ?? 0),
      max: Math.round(w.__perf?.max ?? 0),
    };
  });
}

/**
 * Run `action`, wait for `settle` to observe the DOM actually reflecting it,
 * and return the elapsed wall-clock. The settle step is what makes the number
 * mean "until the user could see the result" rather than "until the event was
 * dispatched".
 */
async function timed(action: () => Promise<void>, settle: () => Promise<void>) {
  const t0 = Date.now();
  await action();
  await settle();
  return Date.now() - t0;
}

/** Repeat an interaction REPS times and record one Sample from the batch. */
async function record(
  page: Page,
  lane: string,
  n: number,
  metric: string,
  note: string,
  once: () => Promise<number>,
): Promise<void> {
  await resetPerf(page);
  const samples: number[] = [];
  for (let i = 0; i < REPS; i++) samples.push(await once());
  const perf = await readPerf(page);
  results.push({
    lane,
    n,
    metric,
    medianMs: median(samples),
    minMs: Math.min(...samples),
    maxMs: Math.max(...samples),
    reps: REPS,
    longTasks: perf.count,
    blockingMs: perf.blocking,
    maxTaskMs: perf.max,
    note,
  });
}

// --- fixtures --------------------------------------------------------------
//
// One generator per lane. Each produces items that survive that lane's OWN
// client-side filter chain with default filters — otherwise the harness would
// seed N and render fewer, and silently measure the wrong row count. The
// per-lane guards that dictate these shapes are cited inline.

/** Dupes: only `id` is strictly required, but book_a/book_b are what make the
 *  row render titles instead of "(missing book — …)", i.e. the real row. */
function dupeCandidates(n: number) {
  const bands = ['CERTAIN', 'HIGH', 'MEDIUM', 'REVIEW'];
  return Array.from({ length: n }, (_, i) => ({
    id: i + 1,
    entity_type: 'book',
    entity_a_id: `01ABCDEFGHIJKLMNOPQRST${String(i).padStart(4, '0')}A`,
    entity_b_id: `01ABCDEFGHIJKLMNOPQRST${String(i).padStart(4, '0')}B`,
    layer: 'embedding',
    similarity: 0.9,
    status: 'pending',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    band: bands[i % bands.length],
    score: 90 + (i % 10),
    book_a: {
      id: `01ABCDEFGHIJKLMNOPQRST${String(i).padStart(4, '0')}A`,
      title: `Dupe Book ${String(i + 1).padStart(4, '0')}`,
      author_name: `Author ${i % 25}`,
      file_path: `/library/a/dupe-${i + 1}.m4b`,
    },
    book_b: {
      id: `01ABCDEFGHIJKLMNOPQRST${String(i).padStart(4, '0')}B`,
      title: `Dupe Book ${String(i + 1).padStart(4, '0')} (copy)`,
      author_name: `Author ${i % 25}`,
      file_path: `/library/b/dupe-${i + 1}.mp3`,
    },
  }));
}

/**
 * Metadata: the default filter chain in useMetadataLane drops almost
 * everything, so this shape is load-bearing:
 *   - `status: 'matched'`  — hideNoMatch defaults TRUE, so 'no_match'/'error'
 *                            are dropped; the reconcile also seeds 'rejected'
 *                            for every no_match row and hideRejected is TRUE.
 *   - `score >= 0.85`      — DEFAULT_CONFIDENCE is 85 and the guard is
 *                            score * 100 >= threshold.
 *   - NO `language` field  — matchLanguage defaults TRUE; it only drops a row
 *                            when BOTH sides carry a language and they differ.
 *                            Omitting it makes the guard a no-op.
 *   - distinct `asin`      — candidateKey() buckets by asin first, and a bucket
 *                            with >1 member becomes a GroupedCard, which would
 *                            make the rendered row count disagree with N.
 */
function metadataResults(n: number) {
  return Array.from({ length: n }, (_, i) => ({
    book: {
      id: `book-${i + 1}`,
      title: `Review Book ${String(i + 1).padStart(4, '0')}`,
      author: `Author ${i % 25}`,
      file_path: `/library/review/book-${i + 1}.m4b`,
    },
    candidate: {
      title: `Review Book ${String(i + 1).padStart(4, '0')}`,
      author: `Author ${i % 25}`,
      source: 'audible',
      score: 0.95,
      asin: `ASIN${String(i).padStart(6, '0')}`,
    },
    status: 'matched',
    fetched_at: '2026-01-01T00:00:00Z',
    is_fresh: true,
  }));
}

/** Regroup: `id` and `kind` are what a row needs; `payload` is a JSON STRING
 *  and is parsed defensively. Three kinds so the buckets are realistic and the
 *  "Kind (A–Z)" default sort has something to order. */
function regroupItems(n: number) {
  const kinds = ['regroup.multidisc', 'regroup.split', 'regroup.merge'];
  return Array.from({ length: n }, (_, i) => ({
    id: `hold-${String(i + 1).padStart(4, '0')}`,
    kind: kinds[i % kinds.length],
    dedup_key: `dk-${i + 1}`,
    folder_ref: `/library/regroup/folder-${i + 1}`,
    status: 'pending',
    summary: `Regroup Hold ${String(i + 1).padStart(4, '0')}`,
    payload: JSON.stringify({ folder: `/library/regroup/folder-${i + 1}` }),
    // Distinct, increasing timestamps so "Newest first" is a real reorder
    // rather than a no-op over ties.
    created_at: new Date(Date.UTC(2026, 0, 1, 0, 0, i)).toISOString(),
    updated_at: new Date(Date.UTC(2026, 0, 1, 0, 0, i)).toISOString(),
  }));
}

// --- route seeding ---------------------------------------------------------
//
// Registered AFTER setupPhase2Interactive so these win: Playwright matches
// handlers most-recently-registered first. That helper's catch-all ends in
// route.continue(), so an un-stubbed review endpoint would hit the REAL server
// and put uncontrolled latency inside a measurement.

/**
 * /api/v1/review/count is polled every 30s by useReviewStore and feeds the
 * regroup lane's bucket totals. Left live it injects recurring network + render
 * noise into every timing below, so it is stubbed on every lane.
 */
async function stubReviewCount(page: Page, byKind: Record<string, number> = {}) {
  await page.route('**/api/v1/review/count', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: {
          count: Object.values(byKind).reduce((a, b) => a + b, 0),
          byKind,
        },
      }),
    }),
  );
}

/**
 * Dupes rows. NOTE: this deliberately ignores the `limit`/`offset` the lane
 * sends and returns exactly N.
 *
 * Dupes pagination is SERVER-side, so "the server returned a page of N rows" is
 * exactly what a real backend does when Rows-per-page is N — the lane itself
 * does no slicing. What this therefore does NOT exercise is the Rows-per-page
 * control itself. Driving that MUI select instead was rejected: this repo has a
 * documented history of MUI menu backdrops wedging mid-close and swallowing
 * clicks (see the MuiMenu note in web/src/theme.ts), and a flaky control click
 * inside a timing loop would corrupt the numbers it is supposed to produce.
 */
async function seedDupes(page: Page, n: number) {
  const candidates = dupeCandidates(n);
  await page.route('**/api/v1/dedup/stats', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: {
          stats: [
            { entity_type: 'book', layer: 'embedding', status: 'pending', count: n },
          ],
        },
      }),
    }),
  );
  await page.route('**/api/v1/dedup/candidates**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: { candidates, total: n } }),
    }),
  );
  await stubReviewCount(page);
}

/**
 * Metadata rows. The lane requests limit=0 ("everything") and paginates
 * CLIENT-side, so N is pinned by seeding exactly N results AND pinning the page
 * size to N in localStorage. 50 and 100 are both in PAGE_SIZE_OPTIONS, so
 * loadReviewPageSize() restores them verbatim rather than clamping.
 */
async function seedMetadata(page: Page, n: number) {
  const resultsPayload = metadataResults(n);
  await page.addInitScript(
    ([key, size]) => {
      try {
        window.localStorage.setItem(key as string, String(size));
      } catch {
        /* private mode — the lane falls back to its default page size */
      }
    },
    [PAGE_SIZE_STORAGE_KEY, n] as const,
  );
  await page.route('**/api/v1/audiobooks/metadata/cache/review**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: {
          results: resultsPayload,
          total_count: n,
          matched: n,
          no_match: 0,
          errors: 0,
        },
      }),
    }),
  );
  await stubReviewCount(page);
}

/** Regroup rows. Flat envelope — GET /review/items uses RespondWithList and has
 *  NO `data` wrapper, unlike every other endpoint stubbed here. */
async function seedRegroup(page: Page, n: number) {
  const items = regroupItems(n);
  await page.route('**/api/v1/review/items**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        items,
        count: items.length,
        limit: 500,
        offset: 0,
        total: items.length,
      }),
    }),
  );
  await stubReviewCount(page, {
    'regroup.multidisc': Math.ceil(n / 3),
    'regroup.split': Math.ceil(n / 3),
    'regroup.merge': Math.floor(n / 3),
  });
}

// --- per-lane row locators -------------------------------------------------

function dupeRows(page: Page): Locator {
  return page.locator('[data-testid^="dupes-row-"]');
}

/**
 * The metadata SPINE has no per-row test id in the default 'compact' view, so
 * rows are counted from the QueueRail list, which does. The two agree as long
 * as no multi-book group forms — which is what the distinct `asin` in
 * metadataResults() guarantees.
 */
function metadataRows(page: Page): Locator {
  return page.locator('[data-testid="queue-list"] > li');
}

function regroupRows(page: Page): Locator {
  return page.locator('[data-testid^="regroup-row-"]');
}

const ROWS: Record<string, (page: Page) => Locator> = {
  dupes: dupeRows,
  metadata: metadataRows,
  regroup: regroupRows,
};

/** goto the lane and wait until exactly N rows have committed to the DOM. */
async function loadLane(page: Page, lane: string, n: number): Promise<number> {
  const t0 = Date.now();
  await page.goto(`/review?lane=${lane}`);
  await ROWS[lane](page).first().waitFor({ timeout: 240_000 });
  await expect.poll(() => ROWS[lane](page).count(), { timeout: 240_000 }).toBe(n);
  return Date.now() - t0;
}

async function seed(page: Page, lane: string, n: number) {
  if (lane === 'dupes') return seedDupes(page, n);
  if (lane === 'metadata') return seedMetadata(page, n);
  return seedRegroup(page, n);
}

// --- interaction drivers ---------------------------------------------------
//
// Each returns the elapsed ms for ONE application of the interaction, including
// the wait for the DOM to reflect it, and leaves the page in a state where it
// can be applied again.

/** Client-side, undebounced substring filter over the loaded page. */
async function dupesFilterOnce(page: Page, n: number): Promise<number> {
  const box = page.getByRole('textbox', { name: 'Search this page' });
  const ms = await timed(
    async () => {
      await box.fill('Dupe Book 0001 (copy)');
    },
    async () => {
      await expect.poll(() => dupeRows(page).count(), { timeout: 60_000 }).toBe(1);
    },
  );
  await box.fill('');
  await expect.poll(() => dupeRows(page).count(), { timeout: 60_000 }).toBe(n);
  return ms;
}

/** Client-side, undebounced regex filter over the loaded results. */
async function metadataFilterOnce(page: Page, n: number): Promise<number> {
  // Role-scoped, not getByLabel: once the field has text QueueRail renders a
  // "Clear title filter" IconButton, and a bare getByLabel('Title filter')
  // matches BOTH it and the input — strict-mode violation on the second rep.
  const box = page.getByRole('textbox', { name: 'Title filter' });
  const ms = await timed(
    async () => {
      await box.fill('^Review Book 0001$');
    },
    async () => {
      await expect.poll(() => metadataRows(page).count(), { timeout: 60_000 }).toBe(1);
    },
  );
  await box.fill('');
  await expect.poll(() => metadataRows(page).count(), { timeout: 60_000 }).toBe(n);
  return ms;
}

/**
 * Client-side but DEBOUNCED 250 ms. The returned ms therefore has a hard 250 ms
 * floor that is a product decision, not a render cost — read blockingMs /
 * maxTaskMs for this one.
 */
async function regroupFilterOnce(page: Page, n: number): Promise<number> {
  const box = page.getByRole('textbox', { name: 'Search loaded holds' });
  const ms = await timed(
    async () => {
      await box.fill('Regroup Hold 0001');
    },
    async () => {
      await expect.poll(() => regroupRows(page).count(), { timeout: 60_000 }).toBe(1);
    },
  );
  await box.fill('');
  await expect.poll(() => regroupRows(page).count(), { timeout: 60_000 }).toBe(n);
  return ms;
}

/**
 * Toggle one row's checkbox on, then back off, measuring only the ON edge.
 *
 * `force: true` is deliberate and it is doing two different jobs.
 *
 * 1. It is the RIGHT instrument for this question. The cost under test is the
 *    state update plus the re-render it triggers — the thing the memo commit
 *    made a claim about. Playwright's actionability checks (visible, stable,
 *    receives-pointer-events) run BEFORE the click and would be billed to the
 *    app inside the timing window, inflating a number that is already close to
 *    the harness's own floor.
 *
 * 2. It is also load-bearing for the metadata lane specifically, and that is a
 *    FINDING, not a convenience. Without `force`, the metadata rail's checkbox
 *    fails actionability at the benchmark project's default 1280x720 viewport:
 *    Playwright reports `<hr class="MuiDivider-root">` and then a
 *    `<div class="MuiStack-root">` intercepting pointer events at the
 *    checkbox's hit point, and retries until the test times out. The dupes
 *    lane's checkbox clicks normally under the identical harness, so this is
 *    specific to QueueRail's layout rather than to the harness.
 *
 *    That is NOT fixed here — this PR is test-only — and it is NOT proven to be
 *    user-visible: Playwright's hit-point rule is stricter than a human with a
 *    mouse, who can click any pixel of the control. It is reported so someone
 *    can confirm it against a real browser.
 */
async function toggleCheckboxOnce(page: Page, selector: string): Promise<number> {
  const box = page.locator(selector);
  const ms = await timed(
    async () => {
      await box.dispatchEvent('click');
    },
    async () => {
      await expect(box).toBeChecked({ timeout: 60_000 });
    },
  );
  await box.dispatchEvent('click');
  await expect(box).not.toBeChecked({ timeout: 60_000 });
  return ms;
}

/**
 * What element actually receives a pointer event aimed at `selector`'s centre.
 *
 * This exists because of a concrete surprise: on the metadata lane a real click
 * at the row checkbox's hit point is swallowed. `check()` retried until the test
 * timed out, and `check({ force: true })` then failed with "Clicking the
 * checkbox did not change its state" — force skips Playwright's own
 * actionability CHECK but the browser still delivers the event to whatever is
 * topmost at those coordinates, so a covered control stays covered.
 *
 * Recording the covering element turns that from a harness workaround into a
 * reported observation: the note column of the checkbox row names whatever is
 * on top.
 *
 * MEASURED VALUES, so the next reader does not have to re-derive them:
 *   dupes     'self' — nothing is covering it; a real click works, and the
 *             dupes rows are driven with a real click in every other respect.
 *   metadata  'none' — `elementFromPoint` returned null, which it does for
 *             coordinates OUTSIDE THE VIEWPORT. So at the benchmark project's
 *             1280x720 the QueueRail checkbox is not on screen at all; it is
 *             only after Playwright scrolls it into view that a MuiDivider /
 *             MuiStack takes the pointer event.
 *
 * That is why the toggle is driven with dispatchEvent rather than a real click:
 * dispatchEvent targets the element directly and bypasses coordinate hit
 * testing. It also proves the handler itself is FINE — the state flips — so
 * whatever is wrong is layout, not logic. Whether a human at a normal window
 * size is actually blocked is NOT established here and needs confirming in a
 * real browser.
 */
async function hitPointOwner(page: Page, selector: string): Promise<string> {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel);
    if (!el) return 'missing';
    const r = el.getBoundingClientRect();
    const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    if (top === el) return 'self';
    if (!top) return 'none';
    const cls = (top.className || '').toString().split(/\s+/).slice(0, 2).join('.');
    return `${top.tagName.toLowerCase()}${cls ? '.' + cls : ''}`;
  }, selector);
}

/**
 * Regroup sort: a pure client-side re-sort of already-loaded rows, with no
 * refetch. Settles on the FIRST row's test id changing, which is what proves
 * the reorder actually committed rather than merely that the select closed.
 */
async function regroupSortOnce(page: Page): Promise<number> {
  // Scoped by test id, NOT by accessible name. RegroupPanel puts
  // `slotProps.select['aria-label'] = 'Sort holds'` on the select, but the
  // TextField also carries `label="Sort"`, and MUI wires the InputLabel through
  // aria-labelledby — which WINS over aria-label in accessible-name
  // computation. So the combobox's name is "Sort", and
  // getByRole('combobox', { name: 'Sort holds' }) never resolves.
  const combo = page.locator('[data-testid="regroup-sort-select"]').getByRole('combobox');
  const firstId = async () =>
    regroupRows(page).first().getAttribute('data-testid');

  const before = await firstId();
  const toNewest = await timed(
    async () => {
      await combo.click();
      await page.getByRole('option', { name: 'Newest first' }).click();
    },
    async () => {
      await expect.poll(firstId, { timeout: 60_000 }).not.toBe(before);
    },
  );

  // Restore for the next repetition.
  const mid = await firstId();
  await combo.click();
  await page.getByRole('option', { name: 'Kind (A–Z)' }).click();
  await expect.poll(firstId, { timeout: 60_000 }).not.toBe(mid);
  return toNewest;
}

// --- the suite -------------------------------------------------------------

test.describe('review lane responsiveness (measurement only)', () => {
  test.skip(process.env.E2E_PERF !== '1', 'set E2E_PERF=1 to run the measurement');
  // Two workers measuring wall-clock on one machine measure each other.
  test.describe.configure({ mode: 'serial' });
  test.beforeEach(() => {
    test.setTimeout(300_000);
  });

  test.afterAll(() => {
    const header =
      '| lane | N | metric | median ms | min | max | reps | long tasks | blocking ms | max task ms | note |';
    const rule = '|---|---|---|---|---|---|---|---|---|---|---|';
    const rows = results.map(
      (r) =>
        `| ${r.lane} | ${r.n} | ${r.metric} | ${r.medianMs} | ${r.minMs} | ` +
        `${r.maxMs} | ${r.reps} | ${r.longTasks} | ${r.blockingMs} | ` +
        `${r.maxTaskMs} | ${r.note} |`,
    );
    console.log(['', 'REVIEW LANE MEASUREMENT TABLE', header, rule, ...rows, ''].join('\n'));
  });

  /**
   * The body shared by every lane/N combination, including the noise floor and
   * the throttled control — so the control runs the IDENTICAL code path and a
   * difference between them cannot be a difference in what was measured.
   */
  async function runLane(page: Page, lane: string, n: number, throttle = 1) {
    const note = throttle > 1 ? `CONTROL ${throttle}x CPU throttle` : '';
    await instrument(page, throttle);
    await setupPhase2Interactive(page);
    await seed(page, lane, n);

    await resetPerf(page);
    const loadMs = await loadLane(page, lane, n);
    const loadPerf = await readPerf(page);
    results.push({
      lane,
      n,
      metric: 'load → N rows interactive',
      medianMs: loadMs,
      minMs: loadMs,
      maxMs: loadMs,
      reps: 1,
      longTasks: loadPerf.count,
      blockingMs: loadPerf.blocking,
      maxTaskMs: loadPerf.max,
      note: note || 'single pass; includes lazy-chunk fetch',
    });

    if (lane === 'dupes') {
      await record(page, lane, n, 'filter (client, undebounced)', note, () =>
        dupesFilterOnce(page, n),
      );
      const dupeCb = 'input[aria-label="Select candidate 1"]';
      const dupeOwner = await hitPointOwner(page, dupeCb);
      await record(
        page,
        lane,
        n,
        'checkbox toggle',
        `${note} hit-point owner: ${dupeOwner}`.trim(),
        () => toggleCheckboxOnce(page, dupeCb),
      );
      // sort: no control exists on this lane.
    } else if (lane === 'metadata') {
      await record(page, lane, n, 'filter (client, undebounced)', note, () =>
        metadataFilterOnce(page, n),
      );
      const metaCb = 'input[aria-label="Select Review Book 0001"]';
      const metaOwner = await hitPointOwner(page, metaCb);
      await record(
        page,
        lane,
        n,
        'checkbox toggle',
        `${note} hit-point owner: ${metaOwner}`.trim(),
        () => toggleCheckboxOnce(page, metaCb),
      );
      // sort: no control exists on this lane.
    } else {
      await record(
        page,
        lane,
        n,
        'filter (client, 250ms debounce)',
        `${note} includes 250ms debounce floor`.trim(),
        () => regroupFilterOnce(page, n),
      );
      await record(page, lane, n, 'sort change (client)', note, () =>
        regroupSortOnce(page),
      );
      // checkbox: this lane has no row selection.
    }
  }

  for (const lane of ['dupes', 'metadata', 'regroup'] as const) {
    // Noise floor: the same path at a row count too small for per-row cost to
    // matter. If N=100 is not clearly above this, the N comparison is not
    // evidence of anything.
    test(`${lane} @ N=${FLOOR_N} (noise floor)`, async ({ page }) => {
      await runLane(page, lane, FLOOR_N);
    });

    for (const n of SIZES) {
      test(`${lane} @ N=${n}`, async ({ page }) => {
        await runLane(page, lane, n);
      });
    }

    // CONTROL: a known-bad input the instrument must respond to.
    test(`${lane} @ N=100 @6x CPU (control)`, async ({ page }) => {
      await runLane(page, lane, 100, 6);
    });
  }

  // CONTROL: the largest row count the regroup lane can be made to render.
  // REGROUP_FETCH_LIMIT is 500, so anything above this is silently truncated.
  test('regroup @ N=500 (control: fetch-limit ceiling)', async ({ page }) => {
    await runLane(page, 'regroup', 500);
  });
});
