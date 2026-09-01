// file: web/src/components/review/lanes/useMetadataLane.test.ts
// version: 1.10.0
// guid: 6b2d9f47-8c05-4e31-a97b-3d40f5a1c862
// last-edited: 2026-09-01
//
// The dialog this hook was lifted from had no tests for any of the behaviour
// below. Two of these guards -- the stale-response discard and the page clamp --
// are the kind that only misbehave under a race or an unusual filter sequence,
// which is exactly the kind a port quietly drops because nothing goes red.

import { renderHook, act, waitFor } from '@testing-library/react';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../../services/api';
import {
  loadReviewPageSize,
  useMetadataLane,
  MAX_REVIEW_PAGE_SIZE,
  STRICT_PRESET,
  DEFAULT_CONFIDENCE,
} from './useMetadataLane';
import { STORAGE_KEYS } from '../../../lib/storageKeys';

vi.mock('../../../services/api');

const toast = vi.fn();

function makeResult(
  id: string,
  overrides: Partial<api.CandidateResult> = {},
  candidate: Partial<api.MetadataCandidate> = {}
): api.CandidateResult {
  return {
    book: { id, title: `Book ${id}`, author: 'A', language: 'en' },
    status: 'matched',
    candidate: {
      source: 'audible',
      title: `Cand ${id}`,
      author: 'A',
      narrator: 'N',
      score: 2.0,
      language: 'en',
      ...candidate,
    },
    ...overrides,
  } as unknown as api.CandidateResult;
}

function reviewPayload(results: api.CandidateResult[]) {
  return {
    results,
    total_count: results.length,
    matched: results.filter((r) => r.status === 'matched').length,
    no_match: results.filter((r) => r.status === 'no_match').length,
    errors: 0,
  };
}

describe('summary reflects what the server says is reviewable', () => {
  it('carries unreviewable through instead of inventing a zero', async () => {
    // The server counts only rows it can actually return, and reports the
    // cache entries it cannot as `unreviewable`. On production that gap was
    // 14,306 held against 5,774 reviewable; the lane must not silently drop it.
    const rows = [makeResult('b1')];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue({
      ...reviewPayload(rows),
      unreviewable: 8532,
    } as Awaited<ReturnType<typeof api.getCachedReviewResults>>);

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.summary.unreviewable).toBe(8532);
  });

  it('carries the by-cause breakdown through', async () => {
    // The total alone cannot say what to do about itself: an orphaned row can
    // only be reaped, a candidateless one can be refetched. The lane must hand
    // the rail the split, not just the sum.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue({
      ...reviewPayload([makeResult('b1')]),
      unreviewable: 8532,
      unreviewable_by_cause: { orphaned: 3354, no_candidates: 5178, decode_errors: 0 },
    } as Awaited<ReturnType<typeof api.getCachedReviewResults>>);

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.summary.unreviewable_by_cause).toEqual({
      orphaned: 3354,
      no_candidates: 5178,
      decode_errors: 0,
    });
  });

  it('leaves the breakdown undefined when the server omits it', async () => {
    // Not zero-filled: "no breakdown available" and "every cause is zero" are
    // different claims, and the rail renders them differently.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue({
      ...reviewPayload([makeResult('b1')]),
      unreviewable: 8532,
    } as Awaited<ReturnType<typeof api.getCachedReviewResults>>);

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.summary.unreviewable_by_cause).toBeUndefined();
  });

  it('carries the stale count through', async () => {
    // Stale rows are reviewable and counted in `total` -- this is a caveat on
    // what is already in the list, not a shortfall. On production 5,771 of
    // 5,774 reviewable rows were past the TTL and nothing said so.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue({
      ...reviewPayload([makeResult('b1')]),
      stale: 5771,
    } as Awaited<ReturnType<typeof api.getCachedReviewResults>>);

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.summary.stale).toBe(5771);
  });

  it('defaults stale to 0 when the server omits it', async () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([makeResult('b1')]) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.summary.stale).toBe(0);
  });

  it('defaults unreviewable to 0 when the server omits it', async () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([makeResult('b1')]) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.summary.unreviewable).toBe(0);
  });
});

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  toast.mockReset();
});

describe('provider source filters', () => {
  it('normalizes provider display names for counts and filtering', async () => {
    // Production cache rows carry human-readable source names (for example
    // "Audible"), while the filter controls carry stable ids ("audible").
    // Comparing them raw rendered every provider chip as zero and hid its rows.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([
        makeResult('audible', {}, { source: 'Audible' }),
        makeResult('google', {}, { source: 'Google Books' }),
        makeResult('open-library', {}, { source: 'Open Library' }),
      ])
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(3));

    expect(result.current.sourceCounts).toMatchObject({
      audible: 1,
      google_books: 1,
      openlibrary: 1,
    });

    act(() => result.current.setFilters({ sourceFilter: 'audible' }));
    expect(result.current.filteredResults.map((row) => row.book.id)).toEqual(['audible']);
  });
});

describe('stale-response discard', () => {
  it('does not let an earlier fetch overwrite a later one', async () => {
    // The failure this prevents: refresh twice quickly, the first request
    // resolves last, and the user is looking at stale rows under a fresh page
    // number with no error anywhere.
    let resolveFirst: (v: unknown) => void = () => {};
    const first = new Promise((r) => {
      resolveFirst = r;
    });

    vi.mocked(api.getCachedReviewResults)
      .mockReturnValueOnce(first as ReturnType<typeof api.getCachedReviewResults>)
      .mockResolvedValueOnce(reviewPayload([makeResult('second')]));

    const { result } = renderHook(() => useMetadataLane(toast));

    // Kick off the second fetch while the first is still in flight.
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.results).toHaveLength(1));
    expect(result.current.results[0].book.id).toBe('second');

    // Now let the FIRST request land. It must be discarded.
    await act(async () => {
      resolveFirst(reviewPayload([makeResult('first'), makeResult('first-b')]));
      await first;
    });

    expect(result.current.results).toHaveLength(1);
    expect(result.current.results[0].book.id).toBe('second');
  });
});

describe('page clamp', () => {
  it('clamps to the last page when a filter shrinks the set beneath it', async () => {
    // Ten matched rows, page size 25 -> but we shrink the page size to make
    // several pages, walk to the last one, then filter the set down.
    const rows = Array.from({ length: 10 }, (_, i) =>
      makeResult(`b${i}`, {}, { score: i < 5 ? 2.0 : 0.5 })
    );
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload(rows));

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(10));

    act(() => result.current.setPageSize(25));
    // Confidence 85 keeps score*100 >= 85; the 0.5-score rows drop out.
    act(() => result.current.setFilters({ confidenceThreshold: 150 }));

    await waitFor(() => expect(result.current.filteredResults).toHaveLength(5));
    // page must never exceed totalPages
    expect(result.current.page).toBeLessThanOrEqual(result.current.totalPages);
  });

  it('never reports a page above totalPages after a filter change', async () => {
    const rows = Array.from({ length: 60 }, (_, i) => makeResult(`b${i}`));
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload(rows));

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(60));

    act(() => result.current.setPageSize(25));
    act(() => result.current.setPage(3));
    expect(result.current.page).toBe(3);

    // Filter to a single title -> one page. The clamp must pull page back.
    act(() => result.current.setFilters({ titleFilter: '^Book b1$' }));
    await waitFor(() => expect(result.current.page).toBe(1));
  });
});

describe('persisted page size', () => {
  it('clamps AND rewrites an out-of-range stored value', () => {
    // The bug this fixes could not be undone from the UI: the size control was
    // inside the dialog, and a stored 250 froze the dialog before you could
    // reach it. Clamping on read is what makes it self-healing.
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE, '250');
    expect(loadReviewPageSize()).toBe(MAX_REVIEW_PAGE_SIZE);
    // Rewritten, so the bad value is gone for good rather than re-clamped.
    expect(window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE)).toBe(
      String(MAX_REVIEW_PAGE_SIZE)
    );
  });

  it('accepts an offered size unchanged', () => {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE, '50');
    expect(loadReviewPageSize()).toBe(50);
  });

  it('falls back to 25 for junk', () => {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE, 'not-a-number');
    expect(loadReviewPageSize()).toBe(25);
  });
});

describe('strict preset', () => {
  it('sets all three members together and persists', async () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload([]));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => result.current.setStrictPreset(true));

    expect(result.current.filters.hideSkipped).toBe(STRICT_PRESET.hideSkipped);
    expect(result.current.filters.hideMultiBook).toBe(STRICT_PRESET.hideMultiBook);
    expect(result.current.filters.confidenceThreshold).toBe(STRICT_PRESET.confidenceThreshold);
    expect(window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_STRICT_PRESET)).toBe('true');
  });

  it('returns the threshold to the default when switched off', async () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload([]));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => result.current.setStrictPreset(true));
    act(() => result.current.setStrictPreset(false));

    expect(result.current.filters.confidenceThreshold).toBe(DEFAULT_CONFIDENCE);
    expect(result.current.filters.hideMultiBook).toBe(false);
  });
});

describe('hideMultiBook', () => {
  it('deselects books it hides, so Apply Selected cannot apply them', async () => {
    // The toggle's tooltip promises this ("takes the hidden books out of Apply
    // Selected too") and it is behaviour, not description. Without it the
    // button's count disagrees with what it does.
    const shared = { asin: 'B01SAME' };
    const rows = [
      makeResult('a', {}, shared),
      makeResult('b', {}, shared),
      makeResult('c', {}, { asin: 'B01OTHER' }),
    ];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload(rows));

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(3));

    act(() => {
      result.current.spineCtx.onToggleSelect('a');
      result.current.spineCtx.onToggleSelect('c');
    });
    expect(result.current.selectedIds.has('a')).toBe(true);

    act(() => result.current.setFilters({ hideMultiBook: true }));

    await waitFor(() => expect(result.current.selectedIds.has('a')).toBe(false));
    // The unambiguous row keeps its selection.
    expect(result.current.selectedIds.has('c')).toBe(true);
  });

  it('spans page boundaries, because a per-page pass would miss the pair', async () => {
    // Two files of one book on opposite sides of a page boundary each look like
    // a singleton to a per-page pass and survive the hide -- the exact case the
    // toggle exists to remove.
    const shared = { asin: 'B01SAME' };
    const rows = [
      makeResult('a', {}, shared),
      ...Array.from({ length: 30 }, (_, i) => makeResult(`filler${i}`, {}, { asin: `X${i}` })),
      makeResult('z', {}, shared),
    ];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload(rows));

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(32));

    act(() => result.current.setPageSize(25));
    act(() => result.current.setFilters({ hideMultiBook: true }));

    await waitFor(() => {
      const ids = result.current.filteredResults.map((r) => r.book.id);
      expect(ids).not.toContain('a');
      expect(ids).not.toContain('z');
    });
  });
});

describe('hideRuntimeDifferences', () => {
  it('defaults off, then hides only rows whose known runtime differs', async () => {
    // A missing duration is unknown, not a mismatch. This is deliberately
    // tested alongside both threshold cases so the switch cannot become a
    // blanket "only rows with duration" filter during a later refactor.
    const rows = [
      makeResult('same', {}, { duration_delta_sec: 600 }),
      makeResult('different', {}, { duration_delta_sec: 601 }),
      makeResult('unknown', {}, {}),
    ];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload(rows));

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(3));

    expect(result.current.filters.hideRuntimeDifferences).toBe(false);
    expect(result.current.filteredResults.map((r) => r.book.id)).toEqual([
      'same',
      'different',
      'unknown',
    ]);

    act(() => result.current.setFilters({ hideRuntimeDifferences: true }));

    await waitFor(() =>
      expect(result.current.filteredResults.map((r) => r.book.id)).toEqual(['same', 'unknown'])
    );
  });
});

describe('grouping', () => {
  it('groups books sharing a candidate and keeps them out of rows', async () => {
    const shared = { asin: 'B01SAME' };
    const rows = [
      makeResult('a', {}, shared),
      makeResult('b', {}, shared),
      makeResult('c', {}, { asin: 'B01OTHER' }),
    ];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload(rows));

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(3));

    expect(result.current.groups).toHaveLength(1);
    expect(result.current.groups[0].results.map((r) => r.book.id).sort()).toEqual(['a', 'b']);
    // Grouped books must not ALSO render as standalone rows.
    expect(result.current.rows.map((r) => r.book.id)).toEqual(['c']);
  });

  it('ungroup pulls one book back out into rows', async () => {
    const shared = { asin: 'B01SAME' };
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([makeResult('a', {}, shared), makeResult('b', {}, shared)])
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.groups).toHaveLength(1));

    act(() => result.current.dispatch({ lane: 'metadata', type: 'ungroup', id: 'a' }));

    await waitFor(() => {
      // With one member split out, the remaining single book is no longer a
      // group at all -- both render as rows.
      expect(result.current.groups).toHaveLength(0);
      expect(result.current.rows.map((r) => r.book.id).sort()).toEqual(['a', 'b']);
    });
  });
});

describe('dispatch', () => {
  it('skip and unskip are separate actions, not one toggle', async () => {
    // The dialog had a single handler that flipped 'skipped' <-> 'pending', so
    // the Skip button and the "Skipped" chip called the same function and meant
    // opposite things. Dispatching twice must not undo the skip.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload([makeResult('a')]));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(1));

    act(() => result.current.dispatch({ lane: 'metadata', type: 'skip', id: 'a' }));
    expect(result.current.spineCtx.rowState('a')).toBe('skipped');

    act(() => result.current.dispatch({ lane: 'metadata', type: 'skip', id: 'a' }));
    expect(result.current.spineCtx.rowState('a')).toBe('skipped');

    act(() => result.current.dispatch({ lane: 'metadata', type: 'unskip', id: 'a' }));
    expect(result.current.spineCtx.rowState('a')).toBe('pending');
  });

  it('marks a rejected row and offers an undo that clears it', async () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload([makeResult('a')]));
    vi.mocked(api.markNoMatch).mockResolvedValue(undefined);
    vi.mocked(api.clearMetadataNoMatch).mockResolvedValue(undefined);

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(1));

    await act(async () => {
      result.current.dispatch({ lane: 'metadata', type: 'reject', id: 'a' });
    });

    expect(api.markNoMatch).toHaveBeenCalledWith('a');
    expect(result.current.spineCtx.rowState('a')).toBe('rejected');

    // The undo is carried on the toast, so a rejection is always reversible.
    const undo = toast.mock.calls.at(-1)?.[2] as { label: string; onClick: () => void };
    expect(undo?.label).toBe('Undo');

    await act(async () => {
      undo.onClick();
      await Promise.resolve();
    });
    await waitFor(() => expect(result.current.spineCtx.rowState('a')).toBe('pending'));
  });

  it('batches rapid single applies into one call', async () => {
    // Applying five rows in a row must not fire five requests.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload(['a', 'b', 'c'].map((id) => makeResult(id)))
    );
    vi.mocked(api.batchApplyFromCache).mockResolvedValue({
      op_id: 'op1',
    } as unknown as Awaited<ReturnType<typeof api.batchApplyFromCache>>);
    // Never resolves: this test is about the debounce, and letting the poll
    // settle would fire the post-apply refresh and update state outside act().
    vi.mocked(api.pollOperationV2).mockReturnValue(
      new Promise(() => {}) as ReturnType<typeof api.pollOperationV2>
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(3));
    // Fake timers only now: the debounce is what we are measuring, and faking
    // before the initial load makes that load resolve outside act().
    vi.useFakeTimers();

    act(() => {
      result.current.dispatch({ lane: 'metadata', type: 'apply', id: 'a' });
      result.current.dispatch({ lane: 'metadata', type: 'apply', id: 'b' });
    });

    // Optimistic: both rows read as applied immediately.
    expect(result.current.spineCtx.rowState('a')).toBe('applied');
    expect(result.current.spineCtx.rowState('b')).toBe('applied');
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(600);
    });

    expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1);
    expect(api.batchApplyFromCache).toHaveBeenCalledWith(['a', 'b'], undefined);
    vi.useRealTimers();
  });

  it('hides a dispatched selected batch immediately', async () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload(['a', 'b'].map((id) => makeResult(id)))
    );
    vi.mocked(api.batchApplyFromCache).mockResolvedValue({
      op_id: 'op-selected',
    } as Awaited<ReturnType<typeof api.batchApplyFromCache>>);
    vi.mocked(api.pollOperationV2).mockReturnValue(
      new Promise(() => {}) as ReturnType<typeof api.pollOperationV2>
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.filteredResults).toHaveLength(2));

    act(() =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['a', 'b'] })
    );

    await waitFor(() =>
      expect(api.batchApplyFromCache).toHaveBeenCalledWith(['a', 'b'], undefined)
    );
    // The server accepted the operation, so the default "Hide applied" filter
    // must remove it immediately; waiting for the worker to finish leaves stale
    // cards on screen for minutes and invites duplicate clicks.
    // The request resolves before applyMany commits its optimistic state;
    // wait for the state transition rather than racing that callback.
    await waitFor(() => expect(result.current.spineCtx.rowState('a')).toBe('applied'));
    expect(result.current.spineCtx.rowState('b')).toBe('applied');
    expect(result.current.filteredResults).toHaveLength(0);
  });

  it('skipAllUnmatched touches no_match and error rows only', async () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([
        makeResult('matched'),
        makeResult('nm', { status: 'no_match' }),
        makeResult('err', { status: 'error' }),
      ])
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(3));

    act(() => result.current.dispatch({ lane: 'metadata', type: 'skipAllUnmatched' }));

    expect(result.current.spineCtx.rowState('nm')).toBe('skipped');
    expect(result.current.spineCtx.rowState('err')).toBe('skipped');
    // The matched row keeps whatever it had -- 'skip all unmatched' is not
    // 'skip everything'.
    expect(result.current.spineCtx.rowState('matched')).not.toBe('skipped');
  });
});

describe('language filter', () => {
  it('shows a candidate when either side has no language set', async () => {
    // A book with no language must still be offered its candidates; hiding it
    // would put new imports permanently out of reach.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([
        makeResult('nolang', { book: { id: 'nolang', title: 'No lang' } } as never, {
          language: 'fr',
        }),
        makeResult('mismatch', {}, { language: 'fr' }),
      ])
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(2));

    act(() => result.current.setFilters({ matchLanguage: true }));

    const ids = result.current.filteredResults.map((r) => r.book.id);
    expect(ids).toContain('nolang'); // unknown on one side -> shown
    expect(ids).not.toContain('mismatch'); // en vs fr -> hidden
  });

  it('treats spelled-out and coded languages as the same', async () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([makeResult('a', {}, { language: 'English' })])
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(1));

    act(() => result.current.setFilters({ matchLanguage: true }));
    expect(result.current.filteredResults).toHaveLength(1);
  });
});

describe('seeding from server status', () => {
  it('seeds applied and rejected without clobbering a session decision', async () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([
        makeResult('applied', { status: 'applied' }),
        makeResult('rejected', { status: 'no_match' }),
        makeResult('fresh'),
      ])
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(3));

    expect(result.current.spineCtx.rowState('applied')).toBe('applied');
    expect(result.current.spineCtx.rowState('rejected')).toBe('rejected');
    expect(result.current.spineCtx.rowState('fresh')).toBeUndefined();

    // A decision made this session survives a refresh that reports the old status.
    act(() => result.current.dispatch({ lane: 'metadata', type: 'skip', id: 'fresh' }));
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.spineCtx.rowState('fresh')).toBe('skipped'));
  });
});

describe('inactive lane', () => {
  it('does not fetch when the lane is not active', () => {
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(reviewPayload([]));
    renderHook(() => useMetadataLane(toast, false));
    expect(api.getCachedReviewResults).not.toHaveBeenCalled();
  });
});

describe('an optimistic apply must be correctable by the server', () => {
  // Regression test for a silent data-integrity bug.
  //
  // applyMany marks every dispatched book `applied` so the default Hide-applied
  // filter clears it immediately, and its comment promised "the terminal poll
  // still refreshes and restores any book the worker ultimately did not apply".
  // It could not: the refresh seeded row state ADD-ONLY
  // (`if (!merged.has(k)) merged.set(k, v)`), so a key it had just written could
  // never be overwritten. A book the background apply failed on stayed hidden
  // behind the default filter with no route back, and the reviewer was never
  // told -- the queue simply looked finished. Bulk apply made that hundreds of
  // books per click.
  beforeEach(() => {
    vi.mocked(api.batchApplyFromCache).mockResolvedValue({
      op_id: 'op-1',
    } as Awaited<ReturnType<typeof api.batchApplyFromCache>>);
  });

  it('restores a row the background apply did not actually apply', async () => {
    const rows = [makeResult('b1'), makeResult('b2')];
    // The server keeps reporting both as `matched` -- i.e. still pending. That
    // is what a failed apply looks like from here.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload(rows) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
    );
    // The poll is held open deliberately. Resolving it immediately made the
    // hide and the restore collapse into one tick, and the final assertion --
    // 2 rows -- then matched the STARTING state as well as the fixed one. The
    // test passed with applyMany's optimistic marking deleted entirely, i.e. it
    // could not observe the behaviour it is named for. Holding the poll lets
    // the two states be asserted separately.
    let finishPoll: () => void = () => {};
    vi.mocked(api.pollOperationV2).mockReturnValue(
      new Promise<void>((resolve) => {
        finishPoll = resolve;
      }) as unknown as ReturnType<typeof api.pollOperationV2>
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.pageResults).toHaveLength(2);

    await act(async () => {
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['b1', 'b2'] });
    });

    // First: they really are hidden. Without this the assertion below is blind.
    await waitFor(() => expect(result.current.pageResults).toHaveLength(0));

    // Then the op finishes, the terminal poll refreshes, and the server's
    // answer -- still `matched`, nothing applied -- must win over the guess.
    await act(async () => {
      finishPoll();
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(result.current.pageResults).toHaveLength(2);
    });
  });

  it('does not turn a skipped row into a rejected one on refresh', async () => {
    // The reconcile's own regression test. Making the server authoritative for
    // no_match is right for a row the reviewer has not judged, and WRONG for one
    // they have: skip is client-only and lands on rows whose server status is
    // exactly no_match, so an unconditional write flipped every skip to
    // rejected. This is the bug the first version of the fix introduced while
    // fixing the other direction.
    //
    // Reaching the bug takes both filters off, and that is not test scaffolding
    // -- it is the actual triage view. A no_match row is hidden by hideNoMatch
    // (default true) AND seeded `rejected` by the reconcile, so hideRejected
    // (default true) hides it a second time. Clearing both is exactly what a
    // reviewer does to work the no-match pile, and `skipAllUnmatched` exists
    // for that pass.
    //
    // hideRejected then goes back ON before the refresh, which is what turns
    // the clobber from a wrong label into a disappearance: `skipped` survives
    // that filter and `rejected` does not.
    const rows = [makeResult('b1', { status: 'no_match', candidate: undefined })];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload(rows) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () => {
      result.current.setFilters({ hideNoMatch: false, hideRejected: false });
    });
    await waitFor(() => expect(result.current.pageResults).toHaveLength(1));
    // Seeded from the server, not judged by anyone yet.
    expect(result.current.spineCtx.rowState('b1')).toBe('rejected');

    await act(async () => {
      result.current.dispatch({ lane: 'metadata', type: 'skip', id: 'b1' });
    });
    expect(result.current.spineCtx.rowState('b1')).toBe('skipped');

    // Back to hiding rejections. The skipped row must survive that.
    await act(async () => {
      result.current.setFilters({ hideRejected: true });
    });
    expect(result.current.pageResults).toHaveLength(1);

    await act(async () => {
      result.current.refresh();
    });
    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(result.current.spineCtx.rowState('b1')).toBe('skipped');
    // The half the user actually sees: had the refresh flipped it back to
    // rejected, hideRejected would have removed it outright and `unskip` with it.
    expect(result.current.pageResults).toHaveLength(1);
  });

  it('still lets the server mark an unjudged no_match row rejected', async () => {
    // The other side of the guard above: narrowing it to "only reconcile an
    // absent or optimistic-applied state" must not stop the server from
    // classifying a row nobody has touched. Without this, the guard could be
    // widened into inertness and nothing would go red.
    const rows = [makeResult('b1', { status: 'no_match', candidate: undefined })];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload(rows) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(result.current.spineCtx.rowState('b1')).toBe('rejected');
  });

  it('keeps a row hidden when the server confirms it was applied', async () => {
    // The other direction: over-correcting would make applied books reappear
    // forever, which is its own bug.
    const pending = [makeResult('b1'), makeResult('b2')];
    const applied = [
      makeResult('b1', { status: 'applied' }),
      makeResult('b2', { status: 'applied' }),
    ];
    vi.mocked(api.getCachedReviewResults)
      .mockResolvedValueOnce(
        reviewPayload(pending) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
      )
      .mockResolvedValue(
        reviewPayload(applied) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
      );
    vi.mocked(api.pollOperationV2).mockResolvedValue(
      {} as Awaited<ReturnType<typeof api.pollOperationV2>>
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['b1', 'b2'] });
    });

    // Wait for the REFRESH, not just for the rows to be hidden. The optimistic
    // marking hides them immediately, so asserting length 0 straight away
    // succeeds before the reconcile this test exists to exercise has run --
    // the expected value would equal the intermediate value and the assertion
    // would be blind. Mutation testing caught exactly that.
    // Anchor on data that can ONLY exist after the refresh has been committed
    // to state. Waiting for the API call count is not enough -- the call having
    // happened says nothing about React having rendered its result, so the
    // assertion below would still be reading the pre-refresh rows.
    await waitFor(() => {
      expect(result.current.results).toHaveLength(2);
      expect(result.current.results.every((r) => r.status === 'applied')).toBe(true);
    });

    expect(result.current.pageResults).toHaveLength(0);
  });

  it('lets a manual Search apply clear the seeded rejection', async () => {
    // The escape hatch this nearly broke. Search is offered ONLY on no_match
    // rows (QueueRail gates the icon on r.status === 'no_match'), and every
    // no_match row has already been seeded `rejected` by the reconcile itself.
    // The dialog applies server-side for real and MetadataPanel then calls
    // refresh() -- so if a local `rejected` were protected unconditionally, the
    // refresh would discard the server's `applied` and the book would stay
    // hidden behind hideRejected with nothing to show the apply had worked.
    //
    // Protecting human decisions and correcting server-derived ones are the
    // same string, which is why provenance is recorded rather than inferred.
    const before = [makeResult('b1', { status: 'no_match', candidate: undefined })];
    const after = [makeResult('b1', { status: 'applied' })];
    vi.mocked(api.getCachedReviewResults)
      .mockResolvedValueOnce(
        reviewPayload(before) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
      )
      .mockResolvedValue(
        reviewPayload(after) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
      );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    // Seeded by the reconcile, not by a person.
    expect(result.current.spineCtx.rowState('b1')).toBe('rejected');

    // What MetadataPanel's onApplied does after the dialog succeeds.
    await act(async () => {
      result.current.refresh();
    });
    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(result.current.spineCtx.rowState('b1')).toBe('applied');
  });

  it('returns a seeded rejection to pending once the server finds a candidate', async () => {
    // The refetch path. A stale row is re-fetched, the server finds a candidate
    // and stops reporting no_match -- the seeded `rejected` is then a mirror of
    // state that no longer exists and must not outlive it. On production nearly
    // every reviewable row is stale, so this is the common case, not an edge.
    const before = [makeResult('b1', { status: 'no_match', candidate: undefined })];
    const after = [makeResult('b1')]; // matched again
    vi.mocked(api.getCachedReviewResults)
      .mockResolvedValueOnce(
        reviewPayload(before) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
      )
      .mockResolvedValue(
        reviewPayload(after) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
      );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.spineCtx.rowState('b1')).toBe('rejected');

    await act(async () => {
      result.current.refresh();
    });
    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(result.current.spineCtx.rowState('b1')).toBeUndefined();
    // And it is reviewable again rather than hidden by hideRejected.
    expect(result.current.pageResults).toHaveLength(1);
  });

  it('does not undo an unreject before the server has caught up', async () => {
    // The guard's comment claims this case and nothing covered it. unreject
    // clears the no-match server-side and sets `pending` locally; the very next
    // refresh can still read the old no_match, and writing `rejected` back over
    // it would flip the undo the reviewer just performed.
    const rows = [makeResult('b1', { status: 'no_match', candidate: undefined })];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload(rows) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
    );
    vi.mocked(api.clearMetadataNoMatch).mockResolvedValue(undefined);

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.spineCtx.rowState('b1')).toBe('rejected');

    await act(async () => {
      result.current.dispatch({ lane: 'metadata', type: 'unreject', id: 'b1' });
    });
    await waitFor(() => expect(result.current.spineCtx.rowState('b1')).toBe('pending'));

    // The server still reports no_match -- it has not caught up yet.
    await act(async () => {
      result.current.refresh();
    });
    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(result.current.spineCtx.rowState('b1')).toBe('pending');
  });

  it('lets the server report an applied book even over a local skip', async () => {
    // `applied` is a fact about what was written, not an opinion about what to
    // do, so it outranks a human decision. A book applied elsewhere (another
    // tab, a background op) must not keep showing as skipped here.
    const before = [makeResult('b1', { status: 'no_match', candidate: undefined })];
    const after = [makeResult('b1', { status: 'applied' })];
    vi.mocked(api.getCachedReviewResults)
      .mockResolvedValueOnce(
        reviewPayload(before) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
      )
      .mockResolvedValue(
        reviewPayload(after) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
      );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      result.current.dispatch({ lane: 'metadata', type: 'skip', id: 'b1' });
    });
    expect(result.current.spineCtx.rowState('b1')).toBe('skipped');

    await act(async () => {
      result.current.refresh();
    });
    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(result.current.spineCtx.rowState('b1')).toBe('applied');
  });

  it('protects a single-row apply from the moment it is marked', async () => {
    // applyOne is not a small applyMany: it marks the row synchronously and
    // then sits behind a 500ms debounce, so the retention has to happen at the
    // mark rather than after the dispatch resolves. Any refresh landing in that
    // gap -- another apply's terminal poll, a filter change -- saw an `applied`
    // row the server still called `matched` and reverted it, which is the
    // flicker ActionBar.tsx rejects useOptimistic to avoid.
    //
    // The refresh below is deliberately fired WITHOUT first waiting for
    // batchApplyFromCache. An earlier version of this test waited for that call
    // and did not test the window at all: the mock resolves immediately, and
    // JS drains the microtask queue before waitFor's macrotask poll hands
    // control back, so the post-dispatch continuation had always already run by
    // the time the test could act. Restoring the pre-fix design left it green.
    // Firing inside the debounce, before any dispatch exists, is the only way
    // to observe the gap.
    const rows = [makeResult('b1'), makeResult('b2')];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload(rows) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
    );
    vi.mocked(api.pollOperationV2).mockReturnValue(
      new Promise(() => {}) as ReturnType<typeof api.pollOperationV2>
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      result.current.dispatch({ lane: 'metadata', type: 'apply', id: 'b1' });
    });
    expect(result.current.pageResults).toHaveLength(1);
    // Still inside the debounce: nothing has been dispatched yet.
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();

    await act(async () => {
      result.current.refresh();
    });

    expect(result.current.spineCtx.rowState('b1')).toBe('applied');
    expect(result.current.pageResults).toHaveLength(1);
  });

  it('does not revert a row while its apply op is still running', async () => {
    // ActionBar.tsx explains at length why reverting mid-op is wrong: the rows
    // flicker back to pending while the server is still working and a reviewer
    // reasonably concludes the apply failed. The in-flight set is what keeps an
    // unrelated refresh (a filter change, a page change) from doing that.
    const rows = [makeResult('b1'), makeResult('b2')];
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload(rows) as Awaited<ReturnType<typeof api.getCachedReviewResults>>
    );
    // A poll that never settles == the op is still running.
    vi.mocked(api.pollOperationV2).mockReturnValue(
      new Promise(() => {}) as ReturnType<typeof api.pollOperationV2>
    );

    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: ['b1', 'b2'] });
    });
    await waitFor(() => expect(result.current.pageResults).toHaveLength(0));

    // An unrelated refresh while the op is in flight must not resurrect them.
    await act(async () => {
      result.current.refresh();
    });
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.pageResults).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// A failed load must be visible
// ---------------------------------------------------------------------------

describe('a failed load is surfaced rather than swallowed', () => {
  it('exposes the failure message instead of looking like an empty queue', async () => {
    // The catch used to be `.catch(() => setLoading(false))`: no state, no
    // toast, not even a console line. A 500 and an empty cache produced byte
    // for byte the same screen, and that screen told the reviewer to go search
    // providers -- advice that cannot possibly help when the server is down.
    vi.mocked(api.getCachedReviewResults).mockRejectedValue(new Error('boom: 500'));
    const { result } = renderHook(() => useMetadataLane(toast, true));

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toBe('boom: 500');
    expect(result.current.results).toEqual([]);
  });

  it('falls back to a readable message when the rejection carries none', async () => {
    vi.mocked(api.getCachedReviewResults).mockRejectedValue('not an Error');
    const { result } = renderHook(() => useMetadataLane(toast, true));

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toMatch(/could not load/i);
  });

  it('clears the error when a retry succeeds', async () => {
    // Without clearing on the NEXT attempt rather than only on success, a
    // successful Retry would leave the failure Alert on screen forever.
    vi.mocked(api.getCachedReviewResults).mockRejectedValueOnce(new Error('boom'));
    const { result } = renderHook(() => useMetadataLane(toast, true));
    await waitFor(() => expect(result.current.error).toBe('boom'));

    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([makeResult('b1')])
    );
    act(() => result.current.refresh());

    // Wait for the retry to SETTLE, not merely to start -- `error` is cleared
    // when the new attempt begins, so waiting on it alone would pass before the
    // response landed and would not prove the Alert stays gone.
    await waitFor(() => expect(result.current.results).toHaveLength(1));
    expect(result.current.error).toBeNull();
  });

  it('ignores a rejection from a superseded fetch', async () => {
    // A request the reviewer has already superseded can still reject later.
    // Letting that write `error` would paint a failure banner over a page that
    // loaded perfectly well -- the same stale-guard the success path has.
    let rejectFirst: (e: Error) => void = () => {};
    vi.mocked(api.getCachedReviewResults).mockReturnValueOnce(
      new Promise((_, rej) => {
        rejectFirst = rej;
      }) as ReturnType<typeof api.getCachedReviewResults>
    );
    const { result } = renderHook(() => useMetadataLane(toast, true));

    // Supersede it with a fetch that succeeds.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue(
      reviewPayload([makeResult('b1')])
    );
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.results).toHaveLength(1));

    // The abandoned request only now fails.
    await act(async () => {
      rejectFirst(new Error('late failure'));
      await Promise.resolve();
    });

    expect(result.current.error).toBeNull();
  });
});
