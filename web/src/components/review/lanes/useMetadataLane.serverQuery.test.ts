// file: web/src/components/review/lanes/useMetadataLane.serverQuery.test.ts
// version: 1.6.0
// guid: 3b6f1d84-7c20-4a95-9e58-c1d70a2b4e63
// last-edited: 2026-10-19
//
// The Review > Metadata lane's server-side query (view=page), the only way the
// lane loads its queue since 02-PR19 retired the full-index mode.
//
// The shared-case test runs the fixture the Go review query is held to
// (internal/server/handlers/testdata/review_query_cases.json) through the
// lane. The lane has no rows to filter, so its mock server answers only the
// request that case's `query` describes and returns nothing for any other. A
// lane that built a different request than the fixture says gets an empty page
// and fails, so the test cannot pass by echoing its own mock.

import * as fs from 'node:fs';
import * as path from 'node:path';
import { createElement } from 'react';
import { renderHook, render, screen, act, waitFor } from '@testing-library/react';
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import * as api from '../../../services/api';
import {
  applyCapMessage,
  DEFAULT_BULK_APPLY_MAX_ITEMS,
  EXPORT_QUEUE_CHANGED_MESSAGE,
  MAX_QUERY_SKIPPED_IDS,
  useMetadataLane,
  type ChipFilter,
  type MetadataLane,
} from './useMetadataLane';
import { SelectionBar } from '../SelectionBar';

vi.mock('../../../services/api');

const toast = vi.fn();

interface FixtureRow {
  bucket: 'reviewable' | 'unreviewable';
  status: string;
  book: { id: string };
  [k: string]: unknown;
}
interface FixtureCase {
  name: string;
  query: Record<string, string>;
  want_ids: string[];
  want_runtime_hidden: number;
  server_only: boolean;
  want_total: number;
}
const fixture = JSON.parse(
  fs.readFileSync(
    path.resolve(
      __dirname,
      '../../../../../internal/server/handlers/testdata/review_query_cases.json'
    ),
    'utf8'
  )
) as { rows: FixtureRow[]; facets: Record<string, unknown>; cases: FixtureCase[] };

const rowById = new Map(fixture.rows.map((r) => [r.book.id, r as unknown as api.CandidateResult]));
const sharedCases = fixture.cases.filter((c) => !c.server_only);

type IndexResponse = Awaited<ReturnType<typeof api.getCachedReviewResults>>;

function indexPayload(rows: api.CandidateResult[]): IndexResponse {
  return {
    results: rows,
    total_count: rows.length,
    matched: 0,
    no_match: 0,
    errors: 0,
  } as unknown as IndexResponse;
}

/** The request a page params object stands for, as a comparable string. */
function paramString(params: Record<string, unknown>): string {
  return Object.entries(params)
    .filter(([k, v]) => k !== 'limit' && k !== 'offset' && v !== undefined)
    .map(([k, v]) => [k, Array.isArray(v) ? v.join(',') : String(v)] as const)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}=${v}`)
    .join('&');
}

/**
 * What the server is asked for by a case: its `query`, minus what the server
 * ignores. A chip view pauses every filter but the Title, so only those two
 * keys can change its answer (the lane sends only those two).
 */
function expectedRequest(query: Record<string, string>): string {
  const kept = query.chip
    ? Object.fromEntries(Object.entries(query).filter(([k]) => k === 'chip' || k === 'q'))
    : query;
  return paramString(kept);
}

/** Put the lane in exactly the state a case's query describes, everything else off. */
async function applyCase(lane: { current: MetadataLane }, query: Record<string, string>) {
  const on = (k: string) => query[k] === 'true';
  act(() =>
    lane.current.setFilters({
      sourceFilter: query.source ?? null,
      titleFilter: query.q ?? '',
      confidenceThreshold: Number(query.min_confidence ?? 0),
      hideApplied: on('hide_applied'),
      hideRejected: on('hide_rejected'),
      hideSkipped: on('hide_skipped'),
      hideNoMatch: on('hide_no_match'),
      hideRuntimeDifferences: on('hide_runtime'),
      matchLanguage: on('match_language'),
      onlyWithTranscription: on('has_transcription'),
      onlyTranscriptionMatched: on('transcription_matched'),
      hideMultiBook: on('hide_multi_book'),
    })
  );
  for (const id of (query.skipped ?? '').split(',').filter(Boolean)) {
    act(() => lane.current.dispatch({ lane: 'metadata', type: 'skip', id }));
  }
  for (const id of (query.ungrouped ?? '').split(',').filter(Boolean)) {
    act(() => lane.current.dispatch({ lane: 'metadata', type: 'ungroup', id }));
  }
  if (query.chip) {
    act(() => lane.current.toggleChipFilter(query.chip as ChipFilter));
  }
}

/** A server that answers only the request each case describes. */
function serveFixturePages() {
  const byRequest = new Map(sharedCases.map((c) => [expectedRequest(c.query), c]));
  const requests: string[] = [];
  vi.mocked(api.getReviewPage).mockImplementation(async (params) => {
    const key = paramString(params as Record<string, unknown>);
    requests.push(key);
    const c = byRequest.get(key);
    const rows = c ? c.want_ids.map((id) => rowById.get(id)!) : [];
    return {
      results: rows,
      total_count: rows.length,
      offset: 0,
      limit: 25,
      generation: 'g1',
      facets: {
        ...fixture.facets,
        runtime_hidden: c?.want_runtime_hidden ?? 0,
      },
    } as unknown as api.ReviewPageResponse;
  });
  const unreviewable = fixture.rows
    .filter((r) => r.bucket === 'unreviewable')
    .map((r) => r.book.id);
  vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
    ids: unreviewable,
    total: unreviewable.length,
    generation: 'g1',
  });
  vi.mocked(api.getCachedReviewResults).mockImplementation(async (ids) => {
    const want = new Set(ids);
    return indexPayload(fixture.rows.filter((r) => want.has(r.book.id)) as never);
  });
  return requests;
}

async function runPageCase(c: FixtureCase, requests: string[]) {
  const { result, unmount } = renderHook(() => useMetadataLane(toast));
  await waitFor(() => expect(result.current.loading).toBe(false));
  await applyCase(result, c.query);
  const want = expectedRequest(c.query);
  await waitFor(() => expect(requests.at(-1)).toBe(want));
  await waitFor(() => expect(result.current.loading).toBe(false));
  const out = {
    ids: result.current.filteredResults.map((r) => r.book.id),
    runtimeHidden: result.current.runtimeHiddenCount,
  };
  unmount();
  return out;
}

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  // Each test sets its own page; the review level default is irrelevant because
  // applyCase writes every filter.
});

describe('the lane against the shared review query cases', () => {
  it('lists the fixture ids and the runtime-hidden count for every shared case', async () => {
    const requests = serveFixturePages();
    let checked = 0;
    for (const c of sharedCases) {
      const page = await runPageCase(c, requests);
      expect(page.ids, `ids: ${c.name}`).toEqual(c.want_ids);
      expect(page.runtimeHidden, `runtime hidden: ${c.name}`).toBe(c.want_runtime_hidden);
      checked += 1;
    }
    expect(checked).toBe(sharedCases.length);
    expect(checked).toBeGreaterThanOrEqual(12);
    process.stdout.write(`shared cases checked against the lane: ${checked}` + '\n');
  }, 120_000);
});

describe('the lane requests', () => {
  function pageOf(ids: string[], total = ids.length, generation = 'g1') {
    return {
      results: ids.map((id) => ({
        book: { id, title: `Title ${id}`, language: 'en' },
        status: 'matched',
        candidate: { source: 'audible', title: 'Work', author: 'Author 01', score: 2 },
        candidate_hash: `h-${id}`,
      })),
      total_count: total,
      offset: 0,
      limit: 25,
      generation,
      facets: { ...fixture.facets, total },
    } as unknown as api.ReviewPageResponse;
  }

  beforeEach(() => {
    vi.mocked(api.getCachedReviewResults).mockImplementation(async (ids) =>
      indexPayload(ids.map((id) => pageOf([id]).results[0]) as api.CandidateResult[])
    );
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('takes the bulk apply cap from the page and never reads the config', async () => {
    // GET /config needs the settings permission, which a reviewer may not
    // hold; a lane that read the cap there would fall back to the default and
    // let a longer apply through its own check.
    vi.mocked(api.getConfig).mockRejectedValue(
      Object.assign(new Error('forbidden'), { status: 403 })
    );
    vi.mocked(api.getReviewPage).mockResolvedValue({
      ...pageOf(['b1']),
      bulk_apply_max_items: 37,
    });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toBeNull();
    expect(api.getConfig).not.toHaveBeenCalled();
    const ids = Array.from({ length: 38 }, (_, i) => `x${i}`);
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(applyCapMessage(37, 38), 'error');
  });

  it('a page without the cap (an older server) keeps the default cap', async () => {
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['b1']));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    const ids = Array.from({ length: DEFAULT_BULK_APPLY_MAX_ITEMS + 1 }, (_, i) => `x${i}`);
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(
      applyCapMessage(DEFAULT_BULK_APPLY_MAX_ITEMS, ids.length),
      'error'
    );
  });

  it('passes the facets ident_counts through to the rail summary', async () => {
    const identCounts = {
      unasked: 7,
      asked_empty: 4,
      has_candidates: 2,
      applied: 1,
      held: 1,
      missing: 11,
      ready: true,
    };
    const page = pageOf(['b1']);
    vi.mocked(api.getReviewPage).mockResolvedValue({
      ...page,
      facets: { ...page.facets, ident_counts: identCounts },
    });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.summary.ident_counts).toEqual(identCounts);
  });

  it('never loads the review index: no index view, no all=true listing, no unreviewable bucket', async () => {
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['b1']));
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({ ids: [], total: 0, generation: 'g1' });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    // A chip that needs the unreviewable bucket and a refresh: the two other
    // places the index lane used to call getCachedReviewResults.
    act(() => result.current.toggleChipFilter('no_candidates'));
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalled());
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.loading).toBe(false));
    await waitFor(() => expect(result.current.detailPending.size).toBe(0));
    const calls = vi.mocked(api.getCachedReviewResults).mock.calls;
    // The only remaining caller is the per-page detail fetch: ids, never a
    // listing, never a view, never the unreviewable bucket.
    expect(calls.length).toBeGreaterThan(0);
    // Its only argument is the list of ids (the signature has no listing,
    // view or bucket to send).
    for (const c of calls) {
      expect(c).toHaveLength(1);
      expect(Array.isArray(c[0])).toBe(true);
    }
  });

  it('a burst of 10 keystrokes inside the debounce window sends exactly 1 request', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['b1']));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(api.getReviewPage).toHaveBeenCalledTimes(1);

    for (let i = 1; i <= 10; i++) {
      act(() => result.current.setFilters({ titleFilter: 'chapter'.slice(0, i) }));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10);
      });
    }
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300);
    });
    await waitFor(() => expect(result.current.loading).toBe(false));

    // 1 at mount, then exactly 1 for the burst.
    expect(api.getReviewPage).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.getReviewPage).mock.calls[1][0].q).toBe('chapter'.slice(0, 7));
  });

  it('an aborted request never updates state', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const pending: Array<{
      signal?: AbortSignal;
      resolve: (v: api.ReviewPageResponse) => void;
    }> = [];
    vi.mocked(api.getReviewPage).mockImplementation(
      (_params, signal) =>
        new Promise<api.ReviewPageResponse>((resolve) => {
          pending.push({ signal, resolve });
        })
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(pending).toHaveLength(1));
    await act(async () => pending[0].resolve(pageOf(['base'])));
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => result.current.setFilters({ titleFilter: 'one' }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(pending).toHaveLength(2);
    act(() => result.current.setFilters({ titleFilter: 'two' }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(pending).toHaveLength(3);
    // The superseded request was aborted; the mock ignores that and answers anyway.
    expect(pending[1].signal?.aborted).toBe(true);

    await act(async () => pending[1].resolve(pageOf(['stale'])));
    expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['base']);
    await act(async () => pending[2].resolve(pageOf(['fresh'])));
    expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['fresh']);
    // And an answer for the aborted one arriving last changes nothing.
    await act(async () => pending[1].resolve(pageOf(['stale-again'])));
    expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['fresh']);
  });

  it('select all over 2 pages selects total ids and holds no row objects', async () => {
    const TOTAL = 40_000;
    const ids = Array.from({ length: TOTAL }, (_, i) => `id-${String(i).padStart(6, '0')}`);
    vi.mocked(api.getReviewPage).mockImplementation(async (params) =>
      pageOf(ids.slice(params.offset ?? 0, (params.offset ?? 0) + (params.limit ?? 25)), TOTAL)
    );
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({ ids, total: TOTAL, generation: 'g1' });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.setPage(2));
    await waitFor(() => expect(result.current.page).toBe(2));
    await waitFor(() => expect(result.current.loading).toBe(false));

    render(
      createElement(SelectionBar, {
        pageIds: result.current.pageResults.map((r) => r.book.id),
        matchingCount: result.current.filteredCount,
        selectedCount: 0,
        allMatchingSelected: false,
        isSelected: () => false,
        onSelectPage: () => {},
        onSelectAllMatching: () => {},
        onClearSelection: () => {},
      })
    );
    expect(screen.getByText('Select all 40,000 matching')).toBeInTheDocument();

    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(TOTAL));
    expect(result.current.allMatchingSelected).toBe(true);
    // Ids only: the lane holds the page's rows and nothing for the other 39,975.
    expect(result.current.heldRows.list).toBeLessThanOrEqual(result.current.pageSize);
    expect(result.current.results.length).toBeLessThanOrEqual(result.current.pageSize);
    process.stdout.write(
      `select all: ${result.current.selectedIds.size} ids selected, ` +
        `${result.current.heldRows.list} list rows held, page size ${result.current.pageSize}` +
        '\n'
    );
  });

  // Round 1 (S1) changed what this test asserts: a page of a new generation
  // landing after "select all" refuses the next apply ONCE and lists the
  // matching books again, so the reviewer recovers with one more click
  // instead of being told to select all again forever. It used to assert the
  // selection was emptied and nothing else happened.
  it('a stale generation refuses to apply', async () => {
    armApply();
    const ids = ['a1', 'a2', 'a3'];
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 3, 'g1'));
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
      ids,
      total: 3,
      generation: 'g1',
      pins: pinsOf(ids),
    });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(3));

    // The queue moves on: the next page response is a new generation.
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 3, 'g2'));
    act(() => result.current.refresh());
    await waitFor(() => expect(api.getReviewPage).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.loading).toBe(false));

    toast.mockClear();
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
      ids,
      total: 3,
      generation: 'g2',
      pins: pinsOf(ids, (id) => `g2-${id}`),
    });
    act(() => result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids }));
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/^The queue changed after you selected all/),
      'warning'
    );
    // Listed again for the current queue, with the current pins...
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.selectedIds.size).toBe(3));
    // ...and the next click goes through with them.
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids })
    );
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    expect(sentPins().a1).toMatchObject({ content_hash: 'h-a1' });
    expect(sentPins().a2).toMatchObject({ content_hash: 'g2-a2' });
    expect(sentPins().a3).toMatchObject({ content_hash: 'g2-a3' });
  });

  it('a write after the last page fetch is left to the per-book pins, never sent hashless', async () => {
    armApply();
    const ids = ['a1', 'a2', 'a3'];
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 3, 'g1'));
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
      ids,
      total: 3,
      generation: 'g1',
      pins: pinsOf(ids),
    });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(3));
    // No page has been fetched since the select-all.
    expect(api.getReviewPage).toHaveBeenCalledTimes(1);
    // A background op now replaces a2's candidate. Nothing on the client can
    // see that (no page is fetched), so the apply goes out with the hash a2
    // was listed with -- which the server refuses as stale_candidate once the
    // candidate behind it has changed (internal/server/batch_apply_owner_review_test.go).
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids })
    );
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    for (const id of ids) {
      expect(sentPins()[id], id).toMatchObject({ origin: 'review_bulk', content_hash: `h-${id}` });
    }
  });

  it('a hand-picked selection is not refused by a new generation', async () => {
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 1, 'g1'));
    vi.mocked(api.batchApplyFromCache).mockResolvedValue({
      op_id: 'op1',
      requested: 1,
      write_back: true,
    });
    vi.mocked(api.pollOperationV2).mockResolvedValue({} as never);
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 1, 'g2'));
    act(() => result.current.refresh());
    await waitFor(() => expect(api.getReviewPage).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['a1'] })
    );
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
  });

  it('rows held in state never exceed the page size', async () => {
    const TOTAL = 40_000;
    const all = Array.from({ length: TOTAL }, (_, i) => `id-${i}`);
    vi.mocked(api.getReviewPage).mockImplementation(async (params) =>
      pageOf(all.slice(params.offset ?? 0, (params.offset ?? 0) + (params.limit ?? 25)), TOTAL)
    );
    let maxList = 0;
    let maxDetails = 0;
    const { result } = renderHook(() => {
      const lane = useMetadataLane(toast);
      maxList = Math.max(maxList, lane.heldRows.list);
      maxDetails = Math.max(maxDetails, lane.heldRows.details);
      return lane;
    });
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.setPageSize(50));
    await waitFor(() => expect(result.current.pageResults).toHaveLength(50));
    for (const p of [2, 3, 4]) {
      act(() => result.current.setPage(p));
      await waitFor(() => expect(result.current.page).toBe(p));
      await waitFor(() => expect(result.current.loading).toBe(false));
      await waitFor(() => expect(result.current.detailPending.size).toBe(0));
    }
    expect(maxList).toBeLessThanOrEqual(50);
    expect(maxDetails).toBeLessThanOrEqual(50);
    process.stdout.write(
      `page mode, 40,000 rows on the server: max list rows held ${maxList}, details ${maxDetails}` +
        '\n'
    );
  });

  it('reports the server refusing a Title pattern under the field and keeps the rows', async () => {
    vi.mocked(api.getReviewPage).mockResolvedValueOnce(pageOf(['b1']));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    vi.mocked(api.getReviewPage).mockRejectedValueOnce(
      Object.assign(new Error('pattern too slow'), { status: 400, data: { field: 'q' } })
    );
    act(() => result.current.setFilters({ titleFilter: 'chapter' }));
    await waitFor(() => expect(result.current.titleFilterError).toBe('pattern too slow'));
    expect(result.current.error).toBeNull();
    expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['b1']);
  });

  it('a 400 about another parameter is a page error even with a Title pattern set', async () => {
    vi.mocked(api.getReviewPage).mockResolvedValueOnce(pageOf(['b1']));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    vi.mocked(api.getReviewPage).mockRejectedValueOnce(
      Object.assign(new Error('limit must be a whole number from 1 to 500'), {
        status: 400,
        data: { error: 'limit must be a whole number from 1 to 500' },
      })
    );
    act(() => result.current.setFilters({ titleFilter: 'chapter' }));
    await waitFor(() =>
      expect(result.current.error).toBe('limit must be a whole number from 1 to 500')
    );
    expect(result.current.titleFilterError).toBeNull();
  });

  // --- review round 1 (PR #3944) ------------------------------------------
  //
  // These began as the reviewer's repros, which asserted the bugs; they are
  // inverted here and were measured red on the PR head before the fixes.

  function armApply() {
    vi.mocked(api.batchApplyFromCache).mockResolvedValue({
      op_id: 'op1',
      requested: 1,
      write_back: true,
    });
    vi.mocked(api.pollOperationV2).mockResolvedValue({} as never);
  }

  /** The pins an ids=all&pins=true response carries: one per id, no origin. */
  function pinsOf(ids: string[], hash = (id: string) => `h-${id}`) {
    return Object.fromEntries(
      ids.map((id) => [
        id,
        { content_hash: hash(id), source: 'audible', title: 'Work', author: 'Author 01' },
      ])
    );
  }

  /** The pins argument of the Nth batchApplyFromCache call. */
  function sentPins(call = 0): Record<string, api.ApplyPin> {
    return vi.mocked(api.batchApplyFromCache).mock.calls[call][2] as Record<string, api.ApplyPin>;
  }

  it('a row hand-picked on page 1 and applied from page 2 is sent with the pin captured at the tick', async () => {
    armApply();
    const all = ['p1', 'p2'];
    vi.mocked(api.getReviewPage).mockImplementation(async (params) =>
      pageOf(all.slice(params.offset ?? 0, (params.offset ?? 0) + (params.limit ?? 25)), 2)
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.setPageSize(1));
    await waitFor(() => expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['p1']));
    act(() => result.current.setSelection(['p1'], true));
    act(() => result.current.setPage(2));
    await waitFor(() => expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['p2']));
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['p1'] })
    );
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    expect(sentPins().p1).toMatchObject({ origin: 'review_bulk', content_hash: 'h-p1' });
  });

  it('select all applies every book with the pin the server listed it with', async () => {
    armApply();
    const ids = ['a1', 'a2', 'a3'];
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 3, 'g1'));
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
      ids,
      total: 3,
      generation: 'g1',
      pins: pinsOf(ids),
    } as api.ReviewMatchingIds);
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(3));
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids })
    );
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    for (const id of ids) {
      expect(sentPins()[id], id).toMatchObject({ origin: 'review_bulk', content_hash: `h-${id}` });
    }
  });

  it('a selection holding a book the lane has no pin for is refused and nothing is sent', async () => {
    armApply();
    const ids = ['a1', 'a2', 'a3'];
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 3, 'g1'));
    // Above the bulk cap the server lists the ids without pins.
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
      ids,
      total: 3,
      generation: 'g1',
      pins_omitted: true,
    } as api.ReviewMatchingIds);
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(3));
    toast.mockClear();
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/^2 selected books were never shown/),
      'warning'
    );
    // Replace parks nothing for an unpinned selection either.
    act(() => result.current.setBulkApplyMode('replace'));
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids })
    );
    expect(result.current.pendingReplace).toBeNull();
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
  });

  it('ids listed at a newer generation than the page apply with their own pins', async () => {
    armApply();
    const ids = ['a1', 'a2', 'a3'];
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(ids, 3, 'g1'));
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
      ids,
      total: 3,
      generation: 'g2',
      pins: pinsOf(ids, (id) => `g2-${id}`),
    } as api.ReviewMatchingIds);
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(3));
    // An off-page id carries the ids response's pin; on-page ids carry what the
    // page shows. Either way every book is pinned and nothing is refused.
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 3, 'g1'));
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids })
    );
    expect(toast).not.toHaveBeenCalledWith('The queue changed; select all again', 'warning');
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    for (const id of ids) {
      expect((sentPins()[id] as api.CandidatePin).content_hash, id).toBeTruthy();
    }
  });

  it('apply and reject inside the selection re-check window are refused', async () => {
    armApply();
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1', 'a2'], 2, 'g1'));
    let answerPrune: (v: api.ReviewMatchingIds) => void = () => {};
    vi.mocked(api.getReviewMatchingIds).mockImplementation(
      () =>
        new Promise<api.ReviewMatchingIds>((resolve) => {
          answerPrune = resolve;
        })
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.setSelection(['a1', 'a2'], true));
    act(() => result.current.setFilters({ confidenceThreshold: 999 }));
    toast.mockClear();
    await act(async () =>
      result.current.dispatch({
        lane: 'metadata',
        type: 'applySelected',
        ids: [...result.current.selectedIds],
      })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    await act(async () => {
      await result.current.rejectSelected();
    });
    expect(api.markNoMatch).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/^The selection is being checked against the new filters/),
      'warning'
    );
    // The check answers: neither book matches the new filters, so neither is kept.
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalled());
    await act(async () => answerPrune({ ids: [], total: 0, generation: 'g1' }));
    await waitFor(() => expect(result.current.selectedIds.size).toBe(0));
  });

  it('Apply page right after a filter change is refused until the new page lands', async () => {
    armApply();
    vi.mocked(api.getReviewPage).mockResolvedValueOnce(pageOf(['a1', 'a2'], 2, 'g1'));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    let land: (v: api.ReviewPageResponse) => void = () => {};
    vi.mocked(api.getReviewPage).mockImplementationOnce(
      () =>
        new Promise<api.ReviewPageResponse>((resolve) => {
          land = resolve;
        })
    );
    // Nothing is selected, so there is no selection to re-check; but the rows
    // on screen (what Apply page sends) still answer the old filters.
    act(() => result.current.setFilters({ confidenceThreshold: 999 }));
    toast.mockClear();
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['a1', 'a2'] })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/^The list has not caught up with the new filters/),
      'warning'
    );
    await waitFor(() => expect(api.getReviewPage).toHaveBeenCalledTimes(2));
    await act(async () => land(pageOf(['a2'], 1, 'g1')));
    await waitFor(() => expect(result.current.loading).toBe(false));
    // A skip changes the request (the skipped list) but not the filters, so it
    // does not hold the next bulk apply up.
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a2'], 1, 'g1'));
    act(() => result.current.dispatch({ lane: 'metadata', type: 'skip', id: 'a1' }));
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['a2'] })
    );
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    expect(sentPins().a2).toMatchObject({ content_hash: 'h-a2' });
  });

  /** An export-sized page (limit 500) of `total` rows at `generation`. */
  function exportPage(offset: number, total: number, generation: string) {
    const n = Math.max(0, Math.min(500, total - offset));
    const ids = Array.from({ length: n }, (_, i) => `x-${offset + i}`);
    return { ...pageOf(ids, total, generation), offset, limit: 500 } as api.ReviewPageResponse;
  }

  it('an export restarts once when the queue changes under it', async () => {
    const gens = ['g1', 'g2', 'g2', 'g2'];
    const exportOffsets: number[] = [];
    vi.mocked(api.getReviewPage).mockImplementation(async (params) => {
      if (params.limit !== 500) return pageOf(['b1'], 1, 'g1');
      exportOffsets.push(params.offset ?? 0);
      return exportPage(params.offset ?? 0, 600, gens[exportOffsets.length - 1]);
    });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    let rows: api.CandidateResult[] = [];
    await act(async () => {
      rows = await result.current.exportRows();
    });
    // Page 2 came back at g2: the first read was dropped and done again.
    expect(exportOffsets).toEqual([0, 500, 0, 500]);
    expect(rows).toHaveLength(600);
    expect(new Set(rows.map((r) => r.book.id)).size).toBe(600);
    expect(result.current.exporting).toBe(false);
  });

  it('an export that sees the queue change twice fails instead of writing a wrong file', async () => {
    let n = 0;
    vi.mocked(api.getReviewPage).mockImplementation(async (params) => {
      if (params.limit !== 500) return pageOf(['b1'], 1, 'g1');
      n += 1;
      return exportPage(params.offset ?? 0, 600, `g${n}`);
    });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    let failure: unknown;
    await act(async () => {
      await result.current.exportRows().catch((e: unknown) => {
        failure = e;
      });
    });
    expect((failure as Error).message).toBe(EXPORT_QUEUE_CHANGED_MESSAGE);
    expect(n).toBe(4);
    expect(result.current.exporting).toBe(false);
  });

  it('a second export aborts the first, and an unmount aborts the one in flight', async () => {
    const reads: AbortSignal[] = [];
    vi.mocked(api.getReviewPage).mockImplementation((params, signal) => {
      if (params.limit !== 500) return Promise.resolve(pageOf(['b1'], 1, 'g1'));
      reads.push(signal!);
      return new Promise<api.ReviewPageResponse>((_resolve, reject) => {
        signal?.addEventListener('abort', () =>
          reject(new DOMException('The operation was aborted.', 'AbortError'))
        );
      });
    });
    const { result, unmount } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    let first: Promise<unknown> = Promise.resolve();
    act(() => {
      first = result.current.exportRows().catch((e: unknown) => e);
    });
    await waitFor(() => expect(result.current.exporting).toBe(true));
    act(() => {
      void result.current.exportRows().catch(() => {});
    });
    expect(reads).toHaveLength(2);
    expect(reads[0].aborted).toBe(true);
    expect(((await first) as Error).name).toBe('AbortError');
    expect(result.current.exporting).toBe(true);
    unmount();
    expect(reads[1].aborted).toBe(true);
  });

  it('the skipped list sent is the most recently skipped, a re-skip included', async () => {
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['b1'], 1, 'g1'));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.setFilters({ hideSkipped: true }));
    const ids = Array.from(
      { length: MAX_QUERY_SKIPPED_IDS + 1 },
      (_, i) => `s${String(i).padStart(3, '0')}`
    );
    act(() => {
      for (const id of ids) result.current.dispatch({ lane: 'metadata', type: 'skip', id });
    });
    // s000 is unskipped and skipped again: it is now the most recent skip, so
    // s001 is the oldest and the one left out.
    act(() => result.current.dispatch({ lane: 'metadata', type: 'unskip', id: 's000' }));
    act(() => result.current.dispatch({ lane: 'metadata', type: 'skip', id: 's000' }));
    await waitFor(() => {
      const last = vi.mocked(api.getReviewPage).mock.calls.at(-1)![0];
      expect(last.skipped).toHaveLength(MAX_QUERY_SKIPPED_IDS);
      expect(last.skipped).toContain('s000');
      expect(last.skipped).not.toContain('s001');
    });
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('most recently skipped'), 'warning');
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('hidden as rejected'), 'warning');
  });

  it('a single-row Apply of a page row with no candidate hash is refused, not sent unpinned', async () => {
    armApply();
    const page = pageOf(['a1'], 1, 'g1');
    delete (page.results[0] as { candidate_hash?: string }).candidate_hash;
    vi.mocked(api.getReviewPage).mockResolvedValue(page);
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    vi.useFakeTimers({ shouldAdvanceTime: true });
    act(() => result.current.dispatch({ lane: 'metadata', type: 'apply', id: 'a1' }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/^This book has no candidate the page can pin/),
      'warning'
    );
  });

  it("a group's Reject right after a filter change is refused until the new page lands", async () => {
    vi.mocked(api.getReviewPage).mockResolvedValueOnce(pageOf(['a1', 'a2'], 2, 'g1'));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    vi.mocked(api.getReviewPage).mockImplementationOnce(
      () => new Promise<api.ReviewPageResponse>(() => {})
    );
    act(() => result.current.setFilters({ confidenceThreshold: 999 }));
    toast.mockClear();
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'rejectGroup', ids: ['a1', 'a2'] })
    );
    expect(api.markNoMatch).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/^The list has not caught up with the new filters/),
      'warning'
    );
    // Let the debounced request go out, so the one-shot never-answering mock
    // is used here and not by the next test's first page.
    await waitFor(() => expect(api.getReviewPage).toHaveBeenCalledTimes(2));
  });

  // --- review round 2 (PR #3944) ------------------------------------------
  //
  // The first three began as the round-2 reviewer's repros (which asserted
  // the bugs) and are inverted here; all of these were measured red first.

  /** Select all a1..a3 (page shows a1 at g1), untick a2, then land a g2 page. */
  async function selectAllUntickA2ThenNewGeneration() {
    armApply();
    vi.mocked(api.markNoMatch).mockResolvedValue(undefined as never);
    const ids = ['a1', 'a2', 'a3'];
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 3, 'g1'));
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
      ids,
      total: 3,
      generation: 'g1',
      pins: pinsOf(ids),
    });
    const hook = renderHook(() => useMetadataLane(toast));
    const { result } = hook;
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(3));
    act(() => result.current.setSelection(['a2'], false));
    expect([...result.current.selectedIds].sort()).toEqual(['a1', 'a3']);
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1'], 3, 'g2'));
    act(() => result.current.refresh());
    await waitFor(() => expect(api.getReviewPage).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.loading).toBe(false));
    // The queue moved: a4 joined it.
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
      ids: [...ids, 'a4'],
      total: 4,
      generation: 'g2',
      pins: pinsOf([...ids, 'a4']),
    });
    toast.mockClear();
    return hook;
  }

  it('the refuse-and-relist never re-adds a book unticked after select all, nor adds one that joined', async () => {
    const { result } = await selectAllUntickA2ThenNewGeneration();
    act(() =>
      result.current.dispatch({
        lane: 'metadata',
        type: 'applySelected',
        ids: [...result.current.selectedIds],
      })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(expect.stringMatching(/1 book joined/), 'info')
    );
    expect([...result.current.selectedIds].sort()).toEqual(['a1', 'a3']);
    await act(async () =>
      result.current.dispatch({
        lane: 'metadata',
        type: 'applySelected',
        ids: [...result.current.selectedIds],
      })
    );
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    expect([...vi.mocked(api.batchApplyFromCache).mock.calls[0][0]].sort()).toEqual(['a1', 'a3']);
  });

  it('Reject selected after the relist never marks a book unticked after select all', async () => {
    const { result } = await selectAllUntickA2ThenNewGeneration();
    await act(async () => {
      await result.current.rejectSelected();
    });
    expect(api.markNoMatch).not.toHaveBeenCalled();
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(toast).toHaveBeenCalledWith(expect.anything(), 'info'));
    await act(async () => {
      await result.current.rejectSelected();
    });
    const marked = vi.mocked(api.markNoMatch).mock.calls.map((c) => c[0]);
    expect(marked.sort()).toEqual(['a1', 'a3']);
  });

  it('Apply page after select all and a new generation applies the page and leaves the select-all list alone', async () => {
    const { result } = await selectAllUntickA2ThenNewGeneration();
    // Apply page sends the page's rows, which carry fresh pins.
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['a1'] })
    );
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.batchApplyFromCache).mock.calls[0][0]).toEqual(['a1']);
    expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(1);
    expect(toast).not.toHaveBeenCalledWith(
      expect.stringMatching(/^The queue changed after you selected all/),
      'warning'
    );
  });

  it('a selected book whose candidate changed while on screen is applied with the candidate last shown', async () => {
    armApply();
    let hash = 'h1';
    const rowsFor = (ids: string[], total: number) => {
      const p = pageOf(ids, total, 'g1');
      p.results.forEach((r) => {
        if (r.book.id === 'p1') (r as { candidate_hash: string }).candidate_hash = `${hash}-p1`;
      });
      return p;
    };
    const all = ['p1', 'p2'];
    vi.mocked(api.getReviewPage).mockImplementation(async (params) =>
      rowsFor(all.slice(params.offset ?? 0, (params.offset ?? 0) + (params.limit ?? 25)), 2)
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.setPageSize(1));
    await waitFor(() => expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['p1']));
    act(() => result.current.setSelection(['p1'], true));
    // A refresh of the same page shows p1 with a new candidate.
    hash = 'h2';
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.pageResults[0]?.candidate_hash).toBe('h2-p1'));
    act(() => result.current.setPage(2));
    await waitFor(() => expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['p2']));
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['p1'] })
    );
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    expect(sentPins().p1).toMatchObject({ content_hash: 'h2-p1' });
  });

  // --- review round 3 (PR #3944) ------------------------------------------
  //
  // The first two began as the round-3 reviewer's repros ZZ-A and ZZ-B (which
  // asserted the bugs) and are inverted here; all were measured red first.

  const applyNow = (result: { current: MetadataLane }) =>
    act(async () =>
      result.current.dispatch({
        lane: 'metadata',
        type: 'applySelected',
        ids: [...result.current.selectedIds],
      })
    );

  it('a failed re-list keeps the selection and its list, says so, and the next click retries', async () => {
    const { result } = await selectAllUntickA2ThenNewGeneration();
    vi.mocked(api.getReviewMatchingIds).mockRejectedValueOnce(new Error('boom'));
    await applyNow(result);
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        expect.stringMatching(
          /^Could not list the matching books again; your selection is unchanged/
        ),
        'error'
      )
    );
    expect(toast).not.toHaveBeenCalledWith(expect.stringMatching(/select all again/), 'warning');
    expect([...result.current.selectedIds].sort()).toEqual(['a1', 'a3']);
    // The next click re-lists (the default mock now answers) instead of
    // refusing the books as unpinned...
    await applyNow(result);
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(toast).toHaveBeenCalledWith(expect.anything(), 'info'));
    // ...and the one after applies the narrowed selection, never a2 or a4.
    await applyNow(result);
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    expect([...vi.mocked(api.batchApplyFromCache).mock.calls[0][0]].sort()).toEqual(['a1', 'a3']);
  });

  it('a re-list superseded by a Select all that fails does not leave the lane refusing', async () => {
    const { result } = await selectAllUntickA2ThenNewGeneration();
    vi.mocked(api.getReviewMatchingIds).mockImplementationOnce(() => new Promise(() => {}));
    await applyNow(result);
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(2));
    vi.mocked(api.getReviewMatchingIds).mockRejectedValueOnce(new Error('x'));
    act(() => result.current.selectAllMatching());
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith('Could not list the matching books.', 'error')
    );
    toast.mockClear();
    // The hung re-list no longer holds the guard: this click re-lists...
    await applyNow(result);
    expect(toast).not.toHaveBeenCalledWith(
      expect.stringMatching(/still being listed again/),
      'warning'
    );
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(4));
    await waitFor(() => expect(toast).toHaveBeenCalledWith(expect.anything(), 'info'));
    // ...and the next one applies.
    await applyNow(result);
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    expect([...vi.mocked(api.batchApplyFromCache).mock.calls[0][0]].sort()).toEqual(['a1', 'a3']);
  });

  it('a second click while the re-list is out is refused without another request', async () => {
    const { result } = await selectAllUntickA2ThenNewGeneration();
    let answer: (v: api.ReviewMatchingIds) => void = () => {};
    vi.mocked(api.getReviewMatchingIds).mockImplementationOnce(
      () =>
        new Promise<api.ReviewMatchingIds>((r) => {
          answer = r;
        })
    );
    await applyNow(result);
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(2));
    toast.mockClear();
    await applyNow(result);
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/still being listed again/),
      'warning'
    );
    expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(2);
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    await act(async () =>
      answer({ ids: ['a1', 'a3'], total: 2, generation: 'g2', pins: pinsOf(['a1', 'a3']) })
    );
    await applyNow(result);
    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
  });

  it('a Clear during the re-list drops the list it returns and shows no count', async () => {
    const { result } = await selectAllUntickA2ThenNewGeneration();
    let answer: (v: api.ReviewMatchingIds) => void = () => {};
    vi.mocked(api.getReviewMatchingIds).mockImplementationOnce(
      () =>
        new Promise<api.ReviewMatchingIds>((r) => {
          answer = r;
        })
    );
    await applyNow(result);
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(2));
    act(() => result.current.clearSelection());
    toast.mockClear();
    await act(async () =>
      answer({
        ids: ['a1', 'a2', 'a3', 'a4'],
        total: 4,
        generation: 'g2',
        pins: pinsOf(['a1', 'a2', 'a3', 'a4']),
      })
    );
    expect(result.current.selectedIds.size).toBe(0);
    expect(toast).not.toHaveBeenCalledWith(expect.anything(), 'info');
    // No list is held: ticking a1 and applying it does not consult one.
    expect(result.current.allMatchingSelected).toBe(false);
  });

  it('a book that joined and was ticked by hand during the re-list is not counted as joined', async () => {
    const { result } = await selectAllUntickA2ThenNewGeneration();
    let answer: (v: api.ReviewMatchingIds) => void = () => {};
    vi.mocked(api.getReviewMatchingIds).mockImplementationOnce(
      () =>
        new Promise<api.ReviewMatchingIds>((r) => {
          answer = r;
        })
    );
    await applyNow(result);
    await waitFor(() => expect(api.getReviewMatchingIds).toHaveBeenCalledTimes(2));
    act(() => result.current.setSelection(['a4'], true));
    toast.mockClear();
    await act(async () =>
      answer({
        ids: ['a1', 'a2', 'a3', 'a4'],
        total: 4,
        generation: 'g2',
        pins: pinsOf(['a1', 'a2', 'a3', 'a4']),
      })
    );
    expect(toast).toHaveBeenCalledWith(expect.stringMatching(/selection is unchanged/), 'info');
    expect(toast).not.toHaveBeenCalledWith(expect.stringMatching(/joined/), 'info');
  });

  it('a selected row the session hid is not counted as seen on screen', async () => {
    armApply();
    const ids = ['a1', 'a2', 'a3'];
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1', 'a2'], 3, 'g1'));
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({
      ids,
      total: 3,
      generation: 'g1',
      pins: pinsOf(ids),
    });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    // a2 is skipped and hidden by Hide skipped before anything is selected.
    act(() => result.current.setFilters({ hideSkipped: true }));
    act(() => result.current.dispatch({ lane: 'metadata', type: 'skip', id: 'a2' }));
    await waitFor(() => expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['a1']));
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(3));
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['a1', 'a2'], 3, 'g2'));
    const before = vi.mocked(api.getReviewPage).mock.calls.length;
    act(() => result.current.refresh());
    await waitFor(() =>
      expect(vi.mocked(api.getReviewPage).mock.calls.length).toBeGreaterThan(before)
    );
    await waitFor(() => expect(result.current.loading).toBe(false));
    toast.mockClear();
    // a2 was never shown, so its only pin is the list's: the stale list is refused.
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['a2'] })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/^The queue changed after you selected all/),
      'warning'
    );
  });

  it('after a failed page fetch a bulk action says the page did not load', async () => {
    armApply();
    vi.mocked(api.getReviewPage).mockResolvedValueOnce(pageOf(['a1'], 1, 'g1'));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    vi.mocked(api.getReviewPage).mockRejectedValueOnce(
      Object.assign(new Error('Failed to load the review page'), { status: 500 })
    );
    act(() => result.current.setFilters({ confidenceThreshold: 999 }));
    await waitFor(() => expect(result.current.error).toBe('Failed to load the review page'));
    toast.mockClear();
    await act(async () =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['a1'] })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/^The page for the current filters did not load/),
      'warning'
    );
  });

  it('does not send a Title pattern its own compiler rejects', async () => {
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['b1']));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.setFilters({ titleFilter: 'title:/(?=x)/' }));
    await act(async () => {
      await new Promise((r) => setTimeout(r, 300));
    });
    expect(result.current.titleFilterError).toBeTruthy();
    for (const call of vi.mocked(api.getReviewPage).mock.calls) {
      expect(call[0].q).toBeUndefined();
    }
  });
});
