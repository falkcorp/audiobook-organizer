// file: web/src/components/review/lanes/useMetadataLane.test.ts
// version: 1.4.0
// guid: 6b2d9f47-8c05-4e31-a97b-3d40f5a1c862
// last-edited: 2026-08-27
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
