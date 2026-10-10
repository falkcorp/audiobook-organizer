// file: web/src/components/review/lanes/useMetadataLane.serverQuery.test.ts
// version: 1.0.0
// guid: 3b6f1d84-7c20-4a95-9e58-c1d70a2b4e63
// last-edited: 2026-10-10
//
// Page mode (setting review_metadata_server_query) of the Review > Metadata
// lane, and the proof that it agrees with index mode.
//
// The parity tests run the shared fixture the Go review query is held to
// (internal/server/handlers/testdata/review_query_cases.json) through BOTH
// lane modes. Index mode filters the fixture rows in the browser; page mode
// has no rows to filter, so its mock server answers only the request that
// case's `query` describes and returns nothing for any other. A lane that
// built a different request than the fixture says gets an empty page and
// fails, so the page-mode half cannot pass by echoing its own mock.

import * as fs from 'node:fs';
import * as path from 'node:path';
import { createElement } from 'react';
import { renderHook, render, screen, act, waitFor } from '@testing-library/react';
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import * as api from '../../../services/api';
import { useMetadataLane, type ChipFilter, type MetadataLane } from './useMetadataLane';
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

/** Serves the fixture as the index listing, both buckets, and the ids= detail fetch. */
function serveFixtureIndex() {
  vi.mocked(api.getCachedReviewResults).mockImplementation(
    async (_l, _o, _a, bucket = 'reviewable', options = {}) => {
      if (options.ids) {
        const want = new Set(options.ids);
        return indexPayload(fixture.rows.filter((r) => want.has(r.book.id)) as never);
      }
      return indexPayload(fixture.rows.filter((r) => r.bucket === bucket) as never);
    }
  );
}

/** The request a page-mode params object stands for, as a comparable string. */
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

async function runIndexCase(c: FixtureCase) {
  vi.mocked(api.getConfig).mockResolvedValue({
    review_metadata_server_query: false,
  } as api.Config);
  serveFixtureIndex();
  const { result, unmount } = renderHook(() => useMetadataLane(toast));
  await waitFor(() => expect(result.current.loading).toBe(false));
  await applyCase(result, c.query);
  // A skip is a local decision; the server's `applied` outranks it on the next
  // refresh, which is what the "applied outranks skipped" case is about.
  if (c.query.skipped) {
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.loading).toBe(false));
  }
  await waitFor(() => expect(result.current.unreviewableLoading).toBe(false));
  const out = {
    ids: result.current.filteredResults.map((r) => r.book.id),
    runtimeHidden: result.current.runtimeHiddenCount,
  };
  unmount();
  return out;
}

/** A page-mode server that answers only the request each case describes. */
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
  vi.mocked(api.getCachedReviewResults).mockImplementation(async (_l, _o, _a, _b, options = {}) => {
    const want = new Set(options.ids ?? []);
    return indexPayload(fixture.rows.filter((r) => want.has(r.book.id)) as never);
  });
  return requests;
}

async function runPageCase(c: FixtureCase, requests: string[]) {
  vi.mocked(api.getConfig).mockResolvedValue({
    review_metadata_server_query: true,
  } as api.Config);
  const { result, unmount } = renderHook(() => useMetadataLane(toast));
  await waitFor(() => expect(result.current.queryMode).toBe('page'));
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

describe('page mode against the shared review query cases', () => {
  it('page mode renders the same ids as index mode for every shared case', async () => {
    const requests = serveFixturePages();
    let compared = 0;
    for (const c of sharedCases) {
      const idx = await runIndexCase(c);
      // Index mode is held to the fixture first: the cases were derived from
      // reading its chain, and this is what shows the reading was right.
      expect(idx.ids, `index mode: ${c.name}`).toEqual(c.want_ids);
      expect(idx.runtimeHidden, `index mode runtime hidden: ${c.name}`).toBe(c.want_runtime_hidden);

      const page = await runPageCase(c, requests);
      expect(page.ids, `page mode: ${c.name}`).toEqual(idx.ids);
      expect(page.runtimeHidden, `page mode runtime hidden: ${c.name}`).toBe(idx.runtimeHidden);
      compared += 1;
    }
    expect(compared).toBe(sharedCases.length);
    expect(compared).toBeGreaterThanOrEqual(12);
    process.stdout.write(`shared cases compared, index mode vs page mode: ${compared}` + '\n');
  }, 120_000);
});

describe('page mode requests', () => {
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
    vi.mocked(api.getConfig).mockResolvedValue({
      review_metadata_server_query: true,
    } as api.Config);
    vi.mocked(api.getCachedReviewResults).mockImplementation(async (_l, _o, _a, _b, options = {}) =>
      indexPayload(
        (options.ids ?? []).map((id) => pageOf([id]).results[0]) as api.CandidateResult[]
      )
    );
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('flag off sends 1 view=index and 0 view=page requests', async () => {
    vi.mocked(api.getConfig).mockResolvedValue({
      review_metadata_server_query: false,
    } as api.Config);
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(indexPayload([]));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    const indexCalls = vi
      .mocked(api.getCachedReviewResults)
      .mock.calls.filter((c) => c[4]?.view === 'index');
    expect(indexCalls).toHaveLength(1);
    expect(api.getReviewPage).not.toHaveBeenCalled();
    expect(api.getReviewMatchingIds).not.toHaveBeenCalled();
    expect(result.current.queryMode).toBe('index');
  });

  it('a config that cannot be read keeps index mode', async () => {
    vi.mocked(api.getConfig).mockRejectedValue(new Error('down'));
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(indexPayload([]));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.queryMode).toBe('index'));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(api.getReviewPage).not.toHaveBeenCalled();
  });

  it('flag on never loads the review index', async () => {
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(['b1']));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    const indexCalls = vi
      .mocked(api.getCachedReviewResults)
      .mock.calls.filter((c) => c[4]?.view === 'index' || c[2] === true);
    expect(indexCalls).toHaveLength(0);
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

  it('a stale generation refuses to apply', async () => {
    const ids = ['a1', 'a2', 'a3'];
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(ids, 3, 'g1'));
    vi.mocked(api.getReviewMatchingIds).mockResolvedValue({ ids, total: 3, generation: 'g1' });
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(3));

    // The queue moves on: the next page response is a new generation.
    vi.mocked(api.getReviewPage).mockResolvedValue(pageOf(ids, 3, 'g2'));
    act(() => result.current.refresh());
    await waitFor(() => expect(api.getReviewPage).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.loading).toBe(false));

    toast.mockClear();
    act(() => result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids }));
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith('The queue changed; select all again', 'warning');
    expect(result.current.selectedIds.size).toBe(0);
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

  it('index mode holds every row in the same harness', async () => {
    const TOTAL = 40_000;
    vi.mocked(api.getConfig).mockResolvedValue({
      review_metadata_server_query: false,
    } as api.Config);
    const rows = pageOf(Array.from({ length: TOTAL }, (_, i) => `id-${i}`)).results;
    vi.mocked(api.getCachedReviewResults).mockImplementation(async (_l, _o, _a, _b, options = {}) =>
      indexPayload(options.ids ? rows.filter((r) => options.ids!.includes(r.book.id)) : rows)
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false), { timeout: 30_000 });
    expect(result.current.heldRows.list).toBe(TOTAL);
    process.stdout.write(
      `index mode, 40,000 rows on the server: list rows held ${result.current.heldRows.list}` + '\n'
    );
  }, 60_000);

  it('reports the server refusing a Title pattern under the field and keeps the rows', async () => {
    vi.mocked(api.getReviewPage).mockResolvedValueOnce(pageOf(['b1']));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    vi.mocked(api.getReviewPage).mockRejectedValueOnce(
      Object.assign(new Error('pattern too slow'), { status: 400 })
    );
    act(() => result.current.setFilters({ titleFilter: 'chapter' }));
    await waitFor(() => expect(result.current.titleFilterError).toBe('pattern too slow'));
    expect(result.current.error).toBeNull();
    expect(result.current.pageResults.map((r) => r.book.id)).toEqual(['b1']);
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
