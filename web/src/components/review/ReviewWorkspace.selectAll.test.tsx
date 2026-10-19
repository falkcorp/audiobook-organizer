// file: web/src/components/review/ReviewWorkspace.selectAll.test.tsx
// version: 1.9.0
// guid: 7a3f0c52-e1d9-4b86-9f24-58c0d6a1b3e7
// last-edited: 2026-10-19
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
import { selectionCheckAnswer } from '../../test/reviewServer';

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

/** Page size the lane asks for; the fake honours `limit`/`offset` as the server does. */
function seed(reviewable: api.CandidateResult[] = [matched('m1')], bulkCap?: number) {
  // The server owns the filtering: `chip` picks the set (no_candidates is the
  // unreviewable bucket, anything else the reviewable rows), without a chip
  // `bucket` does ('any' is both buckets, the Title only), and `q` narrows it
  // by title (`/re/` is a regex, anything else a substring).
  const pick = (
    params?: Pick<api.ReviewSelectionParams, 'q' | 'bucket'> & { chip?: api.ReviewQueryParams['chip'] }
  ) => {
    let base = reviewable;
    if (params?.chip === 'no_candidates') base = emptyRows;
    else if (!params?.chip && params?.bucket === 'any') base = [...reviewable, ...emptyRows];
    else if (!params?.chip && params?.bucket === 'unreviewable') base = emptyRows;
    const q = params?.q;
    if (!q) return base;
    const m = /^\/(.*)\/$/.exec(q);
    const re = m ? new RegExp(m[1]) : null;
    return base.filter((r) => (re ? re.test(r.book.title) : r.book.title.includes(q)));
  };
  vi.mocked(api.getReviewPage).mockImplementation(async (params) => {
    const set = pick(params);
    const offset = params?.offset ?? 0;
    const limit = params?.limit ?? 25;
    return {
      results: set.slice(offset, offset + limit),
      total_count: set.length,
      offset,
      limit,
      generation: 'g1',
      // The cap rides every page, as the server's does.
      ...(bulkCap === undefined ? {} : { bulk_apply_max_items: bulkCap }),
      facets: {
        matched: reviewable.length,
        no_match: 0,
        total: reviewable.length,
        errors: 0,
        stale: 0,
        unreviewable: N,
        unreviewable_by_cause: { orphaned: 0, no_candidates: N, decode_errors: 0 },
        sources: {},
        runtime_hidden: 0,
      },
    } as unknown as api.ReviewPageResponse;
  });
  vi.mocked(api.getReviewMatchingIds).mockImplementation(async (params, _signal, options) => {
    const rows = pick(params);
    const ids = rows.map((r) => r.book.id);
    const out: api.ReviewMatchingIds = { ids, total: ids.length, generation: 'g1' };
    if (options?.pins) {
      // As the server: pins ride along only while the list fits the apply cap.
      if (ids.length > (bulkCap ?? 5000)) out.pins_omitted = true;
      else {
        out.pins = {};
        for (const r of rows) {
          if (!r.candidate || !r.candidate_hash) continue;
          out.pins[r.book.id] = {
            content_hash: r.candidate_hash,
            source: r.candidate.source,
            title: r.candidate.title,
            author: r.candidate.author,
          };
        }
      }
    }
    return out;
  });
  vi.mocked(api.checkReviewSelection).mockImplementation(async (params, ids) => {
    const bucket = params?.bucket ?? 'reviewable';
    const universe =
      bucket === 'any' ? [...reviewable, ...emptyRows] : bucket === 'unreviewable' ? emptyRows : reviewable;
    return selectionCheckAnswer(
      pick(params).map((r) => r.book.id),
      universe.map((r) => r.book.id),
      ids
    );
  });
  vi.mocked(api.getCachedReviewResults).mockImplementation(async (ids) => {
    const want = new Set(ids);
    const results = [...reviewable, ...emptyRows].filter((r) => want.has(r.book.id));
    return { results, total_count: results.length, matched: 0, no_match: 0, errors: 0 } as Awaited<
      ReturnType<typeof api.getCachedReviewResults>
    >;
  });
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getReviewItems).mockResolvedValue({
    items: [],
    count: 0,
    limit: 500,
    offset: 0,
    total: 0,
  });
  vi.mocked(api.getReviewCount).mockResolvedValue({
    applying: 0,
    needs_attention: 0,
    count: 0,
    by_kind: {},
  });
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
  // Inherently heavy: one walk through eight interactions over a 1,203-id
  // selection set, each re-rendering the whole workspace. About 1 s on an idle
  // machine, 10 s with the CPU oversubscribed tenfold (40 busy processes under
  // --maxWorkers=8), so it gets more than the 30 s default instead of a flake.
  // The other tests in this file are not this long.
  it(
    'offers every matching book without ticking the page first, keeps it across pages, and clears',
    { timeout: 90_000 },
    async () => {
      const user = await openNoCandidates();

      // Visible before anything is ticked: the owner could not find it when it
      // only appeared after the page checkbox.
      const bar = screen.getByTestId('main-selection-bar');
      expect(within(bar).getByTestId('main-select-all-matching')).toHaveTextContent(
        'Select all 1,203 matching'
      );
      await user.click(screen.getByTestId('select-page'));
      expect(screen.getByTestId('main-selection-count')).toHaveTextContent('25 selected');
      await user.click(within(bar).getByTestId('main-select-all-matching'));

      expect(screen.getByTestId('selected-count')).toHaveTextContent('1,203 selected');
      expect(screen.getByTestId('main-selection-count')).toHaveTextContent(
        '1,203 selected (all 1,203 matching)'
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
    }
  );

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
    // A selection is background work, every chunk of it.
    expect(calls.every((c) => c.interactive === undefined)).toBe(true);
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
  const many = Array.from({ length: N }, (_, i) =>
    matched(`b${i}`, i < 10 ? `Keep ${i}` : `Book ${i}`)
  );

  beforeEach(() => {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_LEVEL, 'off');
    seed(many);
  });

  it('Apply selected on every matching book goes out in APPLY_CHUNK_SIZE requests', async () => {
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.filteredCount).toBe(N));

    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(N));
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
    const big = Array.from({ length: 5001 }, (_, i) =>
      matched(`c${i}`, i < 5000 ? `Keep c${i}` : `Drop c${i}`)
    );
    seed(big);
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.filteredCount).toBe(5001));

    const all = big.map((r) => r.book.id);
    act(() => result.current.dispatch({ lane: 'metadata', type: 'applySelected', ids: all }));
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(applyCapMessage(5000, 5001), 'error');
    expect(applyCapMessage(5000, 5001)).toBe(
      'Apply is limited to 5,000 books at a time (setting bulk_apply_max_items); ' +
        '5,001 selected — narrow the selection or raise the limit in Settings.'
    );

    // The server lists 5,000 for the narrowed filter and sends their pins
    // (a list over the cap carries none), so the apply is accepted.
    act(() => result.current.setFilters({ titleFilter: '/^Keep/' }));
    await waitFor(() => expect(result.current.filteredCount).toBe(5000));
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(5000));
    act(() =>
      result.current.dispatch({
        lane: 'metadata',
        type: 'applySelected',
        ids: result.current.applicableSelectedIds,
      })
    );
    await waitFor(() =>
      expect(api.batchApplyFromCache).toHaveBeenCalledTimes(5000 / APPLY_CHUNK_SIZE)
    );
  });

  it('uses the cap the server reports, without reading the config', async () => {
    const rows = Array.from({ length: 101 }, (_, i) => matched(`s${i}`));
    seed(rows, 100);
    // A reviewer without the settings permission: GET /config is refused. The
    // cap must come with the page, so the limit still holds for them.
    vi.mocked(api.getConfig).mockRejectedValue(
      Object.assign(new Error('forbidden'), { status: 403 })
    );
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.filteredCount).toBe(101));

    act(() =>
      result.current.dispatch({
        lane: 'metadata',
        type: 'applySelected',
        ids: rows.map((r) => r.book.id),
      })
    );
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(applyCapMessage(100, 101), 'error');
    expect(api.getConfig).not.toHaveBeenCalled();
  });

  it('a filter change drops selected books the new filter no longer matches', async () => {
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.filteredCount).toBe(N));

    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.selectedIds.size).toBe(N));
    act(() => result.current.setPage(3));

    act(() => result.current.setFilters({ titleFilter: '/^Keep/' }));
    await waitFor(() => expect(result.current.selectedIds.size).toBe(10));
    // The list the server gave was for the old filters, so "all matching" is
    // not claimed until it is asked for again; then it is the whole set.
    expect(result.current.allMatchingSelected).toBe(false);
    act(() => result.current.selectAllMatching());
    await waitFor(() => expect(result.current.allMatchingSelected).toBe(true));
    expect(result.current.selectedIds.size).toBe(10);
  });
});

describe('lane: title regex inside a chip view', () => {
  const toast = vi.fn();

  beforeEach(() => {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_LEVEL, 'off');
    seed([matched('m1', 'Alpha one'), matched('m2', 'Beta two')]);
  });

  it('prunes only selections whose title fails the new regex, across chips', async () => {
    const { result } = renderHook(() => useMetadataLane(toast));
    await waitFor(() => expect(result.current.filteredCount).toBe(2));

    // Picked in the matched chip...
    act(() => result.current.toggleChipFilter('matched'));
    act(() => result.current.setSelection(['m1', 'm2'], true));
    // ...and in the no-candidates chip, plus an id with no loaded row.
    act(() => result.current.toggleChipFilter('no_candidates'));
    await waitFor(() => expect(result.current.filteredCount).toBe(N));
    act(() => result.current.setSelection(['e0', 'not-loaded'], true));
    expect(result.current.selectedIds.size).toBe(4);

    // The chip honours the regex...
    act(() => result.current.setFilters({ titleFilter: '/^(Alpha|Book e0$)/' }));
    await waitFor(() =>
      expect(result.current.filteredResults.map((r) => r.book.id)).toEqual(['e0'])
    );
    // ...and the server is asked which books of ANY chip still match the new
    // Title (every book of both buckets, the Title only). The selection is one
    // id set across chips: m2 ("Beta two", picked in the matched chip) no
    // longer matches and is dropped; m1 and e0 still match; the id the server
    // does not list stays, the Title has nothing to say about it.
    await waitFor(() =>
      expect(api.checkReviewSelection).toHaveBeenCalledWith(
        expect.objectContaining({ bucket: 'any', q: '/^(Alpha|Book e0$)/' }),
        expect.arrayContaining(['m1', 'm2', 'e0', 'not-loaded']),
        expect.anything()
      )
    );
    // The check carries the four SELECTED ids, not a library-sized id list.
    expect(api.getReviewMatchingIds).not.toHaveBeenCalledWith(
      expect.objectContaining({ bucket: 'any' }),
      expect.anything()
    );
    await waitFor(() =>
      expect([...result.current.selectedIds].sort()).toEqual(['e0', 'm1', 'not-loaded'])
    );

    // A pattern that does not compile is reported and prunes nothing.
    act(() => result.current.setFilters({ titleFilter: '/(/' }));
    expect(result.current.titleFilterError).not.toBeNull();
    expect([...result.current.selectedIds].sort()).toEqual(['e0', 'm1', 'not-loaded']);
    // The pattern is never sent: the list stays at its last valid answer.
    expect(
      vi.mocked(api.getReviewPage).mock.calls.some((c) => c[0]?.q === '/(/')
    ).toBe(false);

    // RE2 has no lookahead: the shared grammar rejects it visibly (JS would
    // have run it), and it prunes nothing either.
    act(() => result.current.setFilters({ titleFilter: '/^(?!Book)/' }));
    expect(result.current.titleFilterError).toMatch(/lookahead/);
    expect(
      vi.mocked(api.getReviewPage).mock.calls.some((c) => c[0]?.q === '/^(?!Book)/')
    ).toBe(false);
  });
});
