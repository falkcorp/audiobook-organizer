// file: web/src/components/review/ReviewWorkspace.selectAll.test.tsx
// version: 1.1.0
// guid: 7a3f0c52-e1d9-4b86-9f24-58c0d6a1b3e7
// last-edited: 2026-09-27
//
// "Select all N matching" across pages (Gmail pattern), and bulk actions that
// work on that whole set: chunked to what the server accepts, one progress
// count, a visible "N selected" with Clear. The owner's case is ~11,324
// no-candidate books; 1,203 here is enough to cross every chunk boundary.

import { render, screen, waitFor, within, renderHook, act } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../services/api';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';
import {
  APPLY_CHUNK_SIZE,
  applyCapMessage,
  FETCH_CHUNK_SIZE,
  useMetadataLane,
} from './lanes/useMetadataLane';
import { STORAGE_KEYS } from '../../lib/storageKeys';

vi.mock('../../services/api');

const N = 1203;

function matched(id: string, title = `Book ${id}`) {
  return {
    book: { id, title, author: 'A', language: 'en' },
    status: 'matched',
    candidate: { source: 'audible', title: `Cand ${id}`, author: 'A', narrator: 'N', score: 2.0 },
    candidate_hash: `h-${id}`,
    is_fresh: true,
  } as unknown as api.CandidateResult;
}

function empty(id: string) {
  return {
    book: { id, title: `Book ${id}`, author: 'A' },
    status: 'no_candidates',
    is_fresh: true,
  } as unknown as api.CandidateResult;
}

const emptyRows = Array.from({ length: N }, (_, i) => empty(`e${i}`));

function seed(reviewable: api.CandidateResult[] = [matched('m1')]) {
  vi.mocked(api.getCachedReviewResults).mockImplementation(
    async (_l, _o, _a, bucket) =>
      ({
        results: bucket === 'unreviewable' ? emptyRows : reviewable,
        total_count: bucket === 'unreviewable' ? N : reviewable.length,
        matched: reviewable.length,
        no_match: 0,
        errors: 0,
        stale: 0,
        unreviewable: N,
        unreviewable_by_cause: { orphaned: 0, no_candidates: N, decode_errors: 0 },
      }) as unknown as Awaited<ReturnType<typeof api.getCachedReviewResults>>
  );
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getReviewItems).mockResolvedValue({
    items: [],
    count: 0,
    limit: 500,
    offset: 0,
    total: 0,
  });
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, by_kind: {} });
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  let op = 0;
  vi.mocked(api.batchFetchCandidates).mockImplementation(async (req) => ({
    operation_id: `op-${++op}`,
    total_books: req.book_ids?.length ?? 0,
    message: 'metadata candidate fetch started',
  }));
  vi.mocked(api.batchApplyFromCache).mockImplementation(
    async () => ({ op_id: `apply-${++op}` }) as Awaited<ReturnType<typeof api.batchApplyFromCache>>
  );
  vi.mocked(api.pollOperationV2).mockResolvedValue(
    undefined as unknown as Awaited<ReturnType<typeof api.pollOperationV2>>
  );
  vi.mocked(api.markNoMatch).mockResolvedValue(
    undefined as unknown as Awaited<ReturnType<typeof api.markNoMatch>>
  );
}

async function openNoCandidates() {
  const user = userEvent.setup();
  render(
    <MemoryRouter initialEntries={['/review']}>
      <ToastProvider>
        <ReviewWorkspace />
      </ToastProvider>
    </MemoryRouter>
  );
  await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
  await user.click(screen.getByTestId('chip-no_candidates'));
  await waitFor(() =>
    expect(within(screen.getByTestId('queue-list')).getAllByRole('checkbox')).toHaveLength(25)
  );
  return user;
}

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  seed();
});

describe('select all matching', () => {
  it('offers every matching book once the page is ticked, keeps it across pages, and clears', async () => {
    const user = await openNoCandidates();

    await user.click(screen.getByTestId('select-page'));
    const banner = screen.getByTestId('select-all-matching-banner');
    expect(banner).toHaveTextContent('All 25 on this page selected');
    await user.click(within(banner).getByTestId('select-all-matching'));

    expect(screen.getByTestId('selected-count')).toHaveTextContent('1,203 selected');
    expect(screen.getByTestId('select-all-matching-banner')).toHaveTextContent(
      'All 1,203 matching selected'
    );
    expect(screen.getByTestId('search-selected')).toHaveTextContent('Search again (1203)');

    // Page two: still the whole set.
    await user.click(screen.getByRole('button', { name: 'Go to page 2' }));
    expect(screen.getByTestId('selected-count')).toHaveTextContent('1,203 selected');
    expect(screen.getByLabelText('Select Book e25')).toBeChecked();

    // A chip toggle does not drop it either.
    await user.click(screen.getByTestId('chip-matched'));
    expect(screen.getByTestId('selected-count')).toHaveTextContent('1,203 selected');
    await user.click(screen.getByTestId('chip-matched'));

    await user.click(screen.getByTestId('clear-selection'));
    expect(screen.getByTestId('selected-count')).toHaveTextContent('Nothing selected');
  });

  it('Search again sends the whole set in chunks the server accepts, one op each', async () => {
    const user = await openNoCandidates();
    await user.click(screen.getByTestId('select-page'));
    await user.click(screen.getByTestId('select-all-matching'));

    await user.click(screen.getByTestId('search-selected'));

    await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledTimes(2));
    const calls = vi.mocked(api.batchFetchCandidates).mock.calls.map((c) => c[0]);
    expect(calls[0].book_ids).toHaveLength(FETCH_CHUNK_SIZE);
    expect(calls[1].book_ids).toHaveLength(N - FETCH_CHUNK_SIZE);
    expect(calls.every((c) => c.force === true)).toBe(true);
    expect(new Set(calls.flatMap((c) => c.book_ids)).size).toBe(N);
    await waitFor(() => expect(api.pollOperationV2).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.getByTestId('selected-count')).toHaveTextContent('Nothing selected')
    );
  });

  it('Reject selected marks every selected book, a few at a time', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const user = await openNoCandidates();
    await user.click(screen.getByTestId('select-page'));
    await user.click(screen.getByTestId('select-all-matching'));

    await user.click(screen.getByTestId('reject-selected'));

    await waitFor(() => expect(api.markNoMatch).toHaveBeenCalledTimes(N));
    await waitFor(() =>
      expect(screen.getByTestId('selected-count')).toHaveTextContent('Nothing selected')
    );
  });
});

describe('lane: chunked apply and filter-change pruning', () => {
  const toast = vi.fn();
  const many = Array.from({ length: N }, (_, i) => matched(`b${i}`, i < 10 ? `Keep ${i}` : `Book ${i}`));

  beforeEach(() => {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_LEVEL, 'off');
    seed(many);
  });

  it('Apply selected on every matching book goes out in APPLY_CHUNK_SIZE requests', async () => {
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(N));

    act(() => result.current.selectAllMatching());
    expect(result.current.selectedIds.size).toBe(N);
    expect(result.current.allMatchingSelected).toBe(true);

    act(() =>
      result.current.dispatch({
        lane: 'metadata',
        type: 'applySelected',
        ids: result.current.applicableSelectedIds,
      })
    );

    await waitFor(() => expect(api.batchApplyFromCache).toHaveBeenCalledTimes(3));
    const sizes = vi.mocked(api.batchApplyFromCache).mock.calls.map((c) => c[0].length);
    expect(sizes).toEqual([APPLY_CHUNK_SIZE, APPLY_CHUNK_SIZE, N - 2 * APPLY_CHUNK_SIZE]);
    // Every chunk carries its own books' pins and the bulk mode.
    const [ids, , pins, mode] = vi.mocked(api.batchApplyFromCache).mock.calls[2];
    expect(Object.keys(pins ?? {}).sort()).toEqual([...ids].sort());
    expect(mode).toBe('fill');
    await waitFor(() => expect(result.current.selectedIds.size).toBe(0));
  });

  it('refuses an apply above the 5,000 cap with no request, and sends 5,000 in chunks', async () => {
    const big = Array.from({ length: 5001 }, (_, i) => matched(`c${i}`));
    seed(big);
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(5001));

    const all = big.map((r) => r.book.id);
    act(() => result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: all }));
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(applyCapMessage(5000, 5001), 'error');
    expect(applyCapMessage(5000, 5001)).toBe(
      'Apply is limited to 5,000 books at a time (setting bulk_apply_max_items); ' +
        '5,001 selected — narrow the selection or raise the limit in Settings.'
    );

    act(() =>
      result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: all.slice(0, 5000) })
    );
    await waitFor(() =>
      expect(api.batchApplyFromCache).toHaveBeenCalledTimes(5000 / APPLY_CHUNK_SIZE)
    );
  });

  it('uses the cap the server reports', async () => {
    const rows = Array.from({ length: 101 }, (_, i) => matched(`s${i}`));
    seed(rows);
    const base = vi.mocked(api.getCachedReviewResults).getMockImplementation()!;
    vi.mocked(api.getCachedReviewResults).mockImplementation(async (...args) => ({
      ...(await base(...args)),
      bulk_apply_max_items: 100,
    }));
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(101));

    act(() =>
      result.current.dispatch({
        lane: 'metadata',
        type: 'applySelected',
        ids: rows.map((r) => r.book.id),
      })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(applyCapMessage(100, 101), 'error');
  });

  it('a filter change drops selected books the new filter no longer matches', async () => {
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.results).toHaveLength(N));

    act(() => result.current.selectAllMatching());
    act(() => result.current.setPage(3));
    expect(result.current.selectedIds.size).toBe(N);

    act(() => result.current.setFilters({ titleFilter: '^Keep' }));
    await waitFor(() => expect(result.current.selectedIds.size).toBe(10));
    expect(result.current.allMatchingSelected).toBe(true);
  });
});
