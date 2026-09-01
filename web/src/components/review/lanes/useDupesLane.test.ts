// file: web/src/components/review/lanes/useDupesLane.test.ts
// version: 1.4.0
// guid: 4a71c8e2-53d9-4f06-b18a-9e2c7d4a0f53
// last-edited: 2026-09-01
//
// The behaviour under test is mostly the behaviour that a port loses silently:
// eight keyboard shortcuts, a suppression guard, a keep-side decision shared
// with the view, and one refusal that prevents an irreversible bulk merge from
// exceeding the filter on screen. None of it fails visibly when dropped.

import { renderHook, act, waitFor } from '@testing-library/react';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../../services/api';
import type { DupesUrlFilters } from './useDupesLane';
import { useDupesLane, MERGE_ALL_BLOCKED_REASON, DEDUP_SHORTCUTS } from './useDupesLane';

vi.mock('../../../services/api');

const toast = vi.fn();

function makeCandidate(id: number, overrides: Partial<api.DedupCandidate> = {}): api.DedupCandidate {
  return {
    id,
    entity_type: 'book',
    entity_a_id: `a${id}`,
    entity_b_id: `b${id}`,
    layer: 'embedding',
    status: 'pending',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    band: 'HIGH',
    book_a: { id: `a${id}`, title: `Book A${id}` },
    book_b: { id: `b${id}`, title: `Book B${id}` },
    ...overrides,
  } as api.DedupCandidate;
}

function mockList(candidates: api.DedupCandidate[], total = candidates.length) {
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates, total });
}

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockClear();
  mockList([makeCandidate(1), makeCandidate(2)]);
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
});

async function renderLane(urlFilters: DupesUrlFilters = { band: null, entityId: null }) {
  // Rendered through props so a test can change the URL-owned filters the way
  // the router does -- a rerender -- rather than the way state used to let it.
  const view = renderHook(({ uf }) => useDupesLane(toast, true, uf), {
    initialProps: { uf: urlFilters },
  });
  await waitFor(() => expect(view.result.current.loading).toBe(false));
  return view;
}

// ---------------------------------------------------------------------------
// The refusal
// ---------------------------------------------------------------------------

describe('mergeAllFiltered is refused when the filter cannot be transmitted', () => {
  it('does not call the bulk endpoint while both-unmatched is active', async () => {
    // both_unmatched is a property of the two BOOKS, not of the candidate, so
    // the bulk-merge endpoint cannot express it. Sending the rest of the filter
    // would merge a strictly LARGER set than the reviewer is looking at, and a
    // merge cannot be undone from this screen.
    const { result } = await renderLane();
    act(() => result.current.setFilters({ bothUnmatched: true }));
    await waitFor(() => expect(result.current.mergeAllFilteredDisabledReason).toBe(MERGE_ALL_BLOCKED_REASON));

    act(() => result.current.dispatch({ lane: 'dupes', type: 'mergeAllFiltered' }));

    expect(api.bulkMergeDedupCandidates).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(MERGE_ALL_BLOCKED_REASON, 'warning');
  });

  it('guards in dispatch, not only on the button', async () => {
    // A disabled control is an affordance; this is the invariant. If someone
    // later re-enables the button, or a command menu or shortcut dispatches
    // directly, the refusal still has to hold.
    const { result } = await renderLane();
    act(() => result.current.setFilters({ bothUnmatched: true }));
    await waitFor(() => expect(result.current.filters.bothUnmatched).toBe(true));

    // Dispatch directly, bypassing any UI disabled state entirely.
    act(() => result.current.dispatch({ lane: 'dupes', type: 'mergeAllFiltered' }));
    expect(api.bulkMergeDedupCandidates).not.toHaveBeenCalled();
  });

  it('sends band and entity_id when it does run, so the merge matches the screen', async () => {
    // The defect this replaces: band was not in the bulk endpoint's vocabulary,
    // so narrowing to REVIEW and pressing "merge everything matching this
    // filter" merged every pending candidate in the library.
    vi.mocked(api.bulkMergeDedupCandidates).mockResolvedValue({
      attempted: 1,
      merged: 1,
      failed: 0,
    } as unknown as api.BulkMergeDedupResult);
    const { result } = await renderLane({ band: 'REVIEW', entityId: 'book-7' });
    expect(result.current.filters.band).toBe('REVIEW');

    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'mergeAllFiltered' });
    });

    expect(api.bulkMergeDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ band: 'REVIEW', entity_id: 'book-7', status: 'pending' })
    );
  });
});

// ---------------------------------------------------------------------------
// Server-side pagination
// ---------------------------------------------------------------------------

describe('pagination is server-side', () => {
  it('sends limit/offset rather than slicing, and pages are 1-based', async () => {
    const { result } = await renderLane();
    expect(api.getDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ limit: 50, offset: 0 }),
      expect.anything()
    );

    act(() => result.current.setPage(3));
    await waitFor(() =>
      expect(api.getDedupCandidates).toHaveBeenLastCalledWith(
        expect.objectContaining({ limit: 50, offset: 100 }),
        expect.anything()
      )
    );
  });

  it('derives totalPages from the server total, not the loaded page length', async () => {
    // The distinction that makes server pagination work: two rows on screen out
    // of 120 matching. A client-side lane could not tell these apart.
    mockList([makeCandidate(1), makeCandidate(2)], 120);
    const { result } = await renderLane();
    expect(result.current.candidates).toHaveLength(2);
    expect(result.current.totalPages).toBe(3);
  });

  it('resets focus across a page change', async () => {
    // There is a window where `candidates` still holds the PREVIOUS page while
    // the next is in flight. Leaving focus put would aim `m` at a row from the
    // page the reviewer just left -- and `m` merges.
    const { result } = await renderLane();
    act(() => result.current.setFocusedIndex(1));
    expect(result.current.focusedIndex).toBe(1);

    act(() => result.current.setPage(2));
    expect(result.current.focusedIndex).toBe(0);
  });

  it('returns to page 1 when a server-side filter changes', async () => {
    const { result } = await renderLane();
    act(() => result.current.setPage(3));
    await waitFor(() => expect(result.current.page).toBe(3));

    act(() => result.current.setFilters({ bothUnmatched: true }));
    expect(result.current.page).toBe(1);
  });

  it('returns to page 1 when the URL-owned filters change', async () => {
    // The reset used to ride along with setFilters. Now band arrives as a prop,
    // so the reset has to happen on the prop change or a reviewer on page 3
    // clicks a band chip and lands on page 3 of a different, shorter result set.
    const { result, rerender } = await renderLane();
    act(() => result.current.setPage(3));
    await waitFor(() => expect(result.current.page).toBe(3));

    rerender({ uf: { band: 'CERTAIN' as const, entityId: null } });
    expect(result.current.page).toBe(1);
  });

  it('does not reset the page for the client-side search', async () => {
    // Search narrows the LOADED page, so it cannot invalidate the page number.
    const { result } = await renderLane();
    act(() => result.current.setPage(2));
    await waitFor(() => expect(result.current.page).toBe(2));

    act(() => result.current.setFilters({ search: 'foo' }));
    expect(result.current.page).toBe(2);
  });
});

describe('in-flight requests are aborted', () => {
  it('aborts the previous request when the filter changes', async () => {
    const signals: (AbortSignal | undefined)[] = [];
    vi.mocked(api.getDedupCandidates).mockImplementation(async (_params, opts) => {
      signals.push(opts?.signal);
      return { candidates: [], total: 0 };
    });
    const { result } = await renderLane();

    act(() => result.current.setFilters({ bothUnmatched: true }));
    await waitFor(() => expect(signals.length).toBeGreaterThan(1));

    // The first request's signal is aborted rather than merely ignored -- the
    // metadata lane discards late responses by fetch id, which still pays for
    // them.
    expect(signals[0]?.aborted).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Keyboard
// ---------------------------------------------------------------------------

function press(key: string, opts: Partial<KeyboardEventInit> = {}) {
  act(() => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, ...opts }));
  });
}

describe('keyboard shortcuts', () => {
  it('moves focus with j and k, clamped at both ends', async () => {
    const { result } = await renderLane();
    expect(result.current.focusedIndex).toBe(0);

    press('k'); // already at the top
    expect(result.current.focusedIndex).toBe(0);

    press('j');
    expect(result.current.focusedIndex).toBe(1);

    press('j'); // already at the bottom of a two-row page
    expect(result.current.focusedIndex).toBe(1);
  });

  it('merges the focused row on m, keeping the higher-quality side', async () => {
    // The whole reason keepDecision is a shared module: the chip in the view and
    // this shortcut must never disagree about which book survives.
    mockList([
      makeCandidate(1, {
        book_a: { id: 'a1', title: 'Thin' },
        // ASIN outweighs everything else in metadataQuality.
        book_b: { id: 'b1', title: 'Rich', asin: 'B00ABC1234' },
      } as Partial<api.DedupCandidate>),
    ]);
    vi.mocked(api.mergeDedupCandidate).mockResolvedValue(undefined);
    await renderLane();

    press('m');

    await waitFor(() => expect(api.mergeDedupCandidate).toHaveBeenCalledWith(1, 'b1'));
  });

  it('dismisses on d and opens the drawer on Enter', async () => {
    vi.mocked(api.dismissDedupCandidate).mockResolvedValue(undefined);
    const { result } = await renderLane();

    press('Enter');
    expect(result.current.drawerCandidateId).toBe(1);

    press('d');
    await waitFor(() => expect(api.dismissDedupCandidate).toHaveBeenCalledWith(1));
  });

  it('leaves already-decided rows alone', async () => {
    // skip/merge on a merged row is not a no-op server-side; it is an error the
    // reviewer never asked for.
    mockList([makeCandidate(1, { status: 'merged' })]);
    await renderLane();

    press('m');
    press('d');

    expect(api.mergeDedupCandidate).not.toHaveBeenCalled();
    expect(api.dismissDedupCandidate).not.toHaveBeenCalled();
  });

  it('Shift+A selects only what the search left on screen', async () => {
    // The label used to promise "the current page" while the code selected the
    // search-narrowed set. Selecting less than promised is the safe direction,
    // but `merge-selected` is irreversible, so the two must agree exactly.
    const { result } = await renderLane();

    act(() => result.current.setFilters({ search: 'A1' }));
    await waitFor(() => expect(result.current.candidates).toHaveLength(1));

    act(() => result.current.selectAllVisible());
    expect([...result.current.selectedIds]).toEqual([1]);
  });

  it('selects with s and every visible row with Shift+A', async () => {
    const { result } = await renderLane();

    press('s');
    expect(result.current.selectedIds.has(1)).toBe(true);

    press('A', { shiftKey: true });
    expect(result.current.selectedIds.size).toBe(2);
  });

  it('does not fire while a text field has focus', async () => {
    // Load-bearing in the workspace in a way it was not in the standalone tab:
    // the queue rail is full of text fields, so without this, typing "m" in a
    // search box merges a candidate.
    await renderLane();
    const input = document.createElement('input');
    document.body.appendChild(input);
    input.focus();

    press('m');
    expect(api.mergeDedupCandidate).not.toHaveBeenCalled();

    input.remove();
  });

  it('still closes the drawer with Escape from inside the drawer', async () => {
    // MUI gives the drawer's paper role="dialog" and moves focus into it, so the
    // suppression guard would otherwise swallow the one key meant to close it.
    const { result } = await renderLane();
    press('Enter');
    expect(result.current.drawerCandidateId).toBe(1);

    const dialog = document.createElement('div');
    dialog.setAttribute('role', 'dialog');
    const inner = document.createElement('button');
    dialog.appendChild(inner);
    document.body.appendChild(dialog);
    inner.focus();

    press('Escape');
    expect(result.current.drawerCandidateId).toBeNull();

    dialog.remove();
  });
});

// ---------------------------------------------------------------------------
// Lane lifecycle
// ---------------------------------------------------------------------------

describe('an inactive lane', () => {
  it('neither fetches nor answers the keyboard', async () => {
    // Both halves matter. A background fetch is waste; a live window listener is
    // a bug, because `j` would move a focus ring on a lane nobody is looking at.
    renderHook(() => useDupesLane(toast, false));
    press('m');

    expect(api.getDedupCandidates).not.toHaveBeenCalled();
    expect(api.mergeDedupCandidate).not.toHaveBeenCalled();
  });
});

describe('client-side search', () => {
  it('narrows the loaded page without refetching', async () => {
    mockList([
      makeCandidate(1, { book_a: { id: 'a1', title: 'Dune' } } as Partial<api.DedupCandidate>),
      makeCandidate(2, { book_a: { id: 'a2', title: 'Neuromancer' } } as Partial<api.DedupCandidate>),
    ]);
    const { result } = await renderLane();
    const callsBefore = vi.mocked(api.getDedupCandidates).mock.calls.length;

    act(() => result.current.setFilters({ search: 'neuro' }));

    expect(result.current.candidates).toHaveLength(1);
    expect(vi.mocked(api.getDedupCandidates).mock.calls.length).toBe(callsBefore);
  });
});

describe('selection', () => {
  it('extends a range on shift-click and adds to what is already selected', async () => {
    mockList([makeCandidate(1), makeCandidate(2), makeCandidate(3), makeCandidate(4)]);
    const { result } = await renderLane();

    act(() => result.current.toggleSelect(1, 0));
    act(() => result.current.toggleSelect(3, 2, true));

    // The span, inclusive of both ends.
    expect([...result.current.selectedIds].sort()).toEqual([1, 2, 3]);

    // Adds rather than replaces, so a selection can be assembled from several
    // ranges -- the behaviour of every file list.
    act(() => result.current.toggleSelect(4, 3));
    expect(result.current.selectedIds.has(4)).toBe(true);
    expect(result.current.selectedIds.has(1)).toBe(true);
  });
});

describe('stats', () => {
  it('reports the pending total and does NOT claim a per-band breakdown', async () => {
    // The stats endpoint groups by entity_type/layer/status; there is no band
    // dimension in the schema. The source shipped a deriveBandCounts that
    // hardcoded zero for every band, so only the total was ever real.
    vi.mocked(api.getDedupStats).mockResolvedValue({
      stats: [
        { entity_type: 'book', layer: 'embedding', status: 'pending', count: 30 },
        { entity_type: 'book', layer: 'exact', status: 'pending', count: 12 },
        { entity_type: 'book', layer: 'exact', status: 'merged', count: 99 },
      ],
    });
    const { result } = await renderLane();

    await waitFor(() => expect(result.current.pendingTotal).toBe(42));
    expect(result.current).not.toHaveProperty('bandCounts');
  });

  it('keeps the lane usable when stats fail', async () => {
    // Band counts are decoration on the filter chips; the candidates are the
    // page. A stats failure must not blank the lane.
    vi.mocked(api.getDedupStats).mockRejectedValue(new Error('stats down'));
    const { result } = await renderLane();

    expect(result.current.candidates).toHaveLength(2);
    expect(result.current.error).toBeNull();
  });
});

describe('bulk actions over a selection', () => {
  it('dismisses each selected id, then clears the selection', async () => {
    vi.mocked(api.dismissDedupCandidate).mockResolvedValue(undefined);
    const { result } = await renderLane();

    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'dismissSelected', ids: [1, 2] });
    });

    expect(api.dismissDedupCandidate).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(result.current.selectedIds.size).toBe(0));
  });

  it('ignores an empty selection rather than calling the endpoint', async () => {
    const { result } = await renderLane();
    act(() => result.current.dispatch({ lane: 'dupes', type: 'mergeSelected', ids: [] }));
    expect(api.mergeDedupCandidate).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Optimistic suppression -- the double-dispatch guard
//
// These cover the race that keyboard-speed triage makes routine rather than
// rare: a decision is async, the refetch that reflects it is slower still, and
// between the two the deciding row is unchanged and still reads `pending`.
// ---------------------------------------------------------------------------

describe('a dispatched decision suppresses its row immediately', () => {
  it('does not merge the same pair twice when m is pressed before the first lands', async () => {
    // The defect this exists to prevent: without suppression BOTH presses read
    // visible[0], so candidate 1 is merged twice -- irreversibly -- and
    // candidate 2, which the reviewer believes they just decided, stays
    // pending and unnoticed.
    mockList([makeCandidate(1), makeCandidate(2)]);
    // Typed as a callable rather than `| null` so control-flow analysis does
    // not narrow it to `never`: TS cannot see the Promise executor run.
    let release: () => void = () => {};
    vi.mocked(api.mergeDedupCandidate).mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          release = () => resolve(undefined);
        })
    );
    await renderLane();

    press('m'); // decides candidate 1; its row leaves `visible` at once
    press('m'); // must therefore land on candidate 2

    expect(vi.mocked(api.mergeDedupCandidate).mock.calls.map((c) => c[0])).toEqual([1, 2]);
    release();
  });

  it('advances focus by removing the row, not by moving the pointer', async () => {
    // Auto-advance falls out of the suppression rather than being a second
    // mechanism that could disagree with it. focusedIndex never moves.
    mockList([makeCandidate(1), makeCandidate(2)]);
    vi.mocked(api.mergeDedupCandidate).mockReturnValue(new Promise<void>(() => {}));
    const { result } = await renderLane();
    expect(result.current.candidates.map((c) => c.id)).toEqual([1, 2]);

    press('m');

    expect(result.current.focusedIndex).toBe(0);
    expect(result.current.candidates.map((c) => c.id)).toEqual([2]);
  });

  it('keeps the keyboard live after the LAST row is decided', async () => {
    // Deciding the bottom row shortens `visible` under a focus pointer aimed at
    // it. Left unclamped, visible[focusedIndex] is undefined and the handler's
    // `if (!focused) return` makes every shortcut a silent no-op -- the lane
    // looks frozen until a refetch lands, which is exactly the stall this
    // change removes. Clamping in an effect rather than in the handler is what
    // keeps the keys alive rather than merely safe.
    mockList([makeCandidate(1), makeCandidate(2)]);
    vi.mocked(api.mergeDedupCandidate).mockReturnValue(new Promise<void>(() => {}));
    vi.mocked(api.dismissDedupCandidate).mockReturnValue(new Promise<void>(() => {}));
    const { result } = await renderLane();

    press('j');
    expect(result.current.focusedIndex).toBe(1);

    press('m'); // decides candidate 2, the last row

    await waitFor(() => expect(result.current.focusedIndex).toBe(0));
    expect(result.current.candidates.map((c) => c.id)).toEqual([1]);

    // The proof that matters: the keyboard still acts on the row that is left.
    press('d');
    expect(api.dismissDedupCandidate).toHaveBeenCalledWith(1);
  });

  it('puts the row back when the decision fails', async () => {
    // An optimistic removal that outlives its failed request drops the pair out
    // of the queue silently -- the merge did not happen, but the reviewer never
    // sees the row again.
    mockList([makeCandidate(1), makeCandidate(2)]);
    vi.mocked(api.mergeDedupCandidate).mockRejectedValue(new Error('conflict'));
    const { result } = await renderLane();

    press('m');

    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(expect.stringContaining('conflict'), 'error')
    );
    expect(result.current.candidates.map((c) => c.id)).toEqual([1, 2]);
  });

  it('keeps a row suppressed while the refetch still reports it pending', async () => {
    // Decisions and refetches interleave. The refetch triggered by this merge
    // was issued against server state that still had the row pending, so it
    // comes back. Retiring the suppression on any refetch -- rather than only
    // when the server stops returning the row -- would resurrect it as pending
    // and re-arm the double-merge.
    const stale = [makeCandidate(1), makeCandidate(2)];
    vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: stale, total: 2 });
    vi.mocked(api.mergeDedupCandidate).mockResolvedValue(undefined);
    const { result } = await renderLane();

    press('m');

    // Wait for the merge to land and its refetch to be issued.
    await waitFor(() => expect(api.getDedupCandidates).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(result.current.candidates.map((c) => c.id)).toEqual([2]);
  });

  it('retires the suppression once the server stops returning the row', async () => {
    // The other half: the set is self-healing and does not grow across a
    // session. Once the server agrees the row is gone, nothing local remains.
    vi.mocked(api.getDedupCandidates)
      .mockResolvedValueOnce({ candidates: [makeCandidate(1), makeCandidate(2)], total: 2 })
      .mockResolvedValue({ candidates: [makeCandidate(2)], total: 1 });
    vi.mocked(api.mergeDedupCandidate).mockResolvedValue(undefined);
    const { result } = await renderLane();

    press('m');
    await waitFor(() => expect(api.getDedupCandidates).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.candidates.map((c) => c.id)).toEqual([2]));

    // Candidate 1 is absent because the SERVER omits it now, not because a
    // local set still hides it -- so a re-appearing id would be shown again.
    vi.mocked(api.getDedupCandidates).mockResolvedValue({
      candidates: [makeCandidate(1), makeCandidate(2)],
      total: 2,
    });
    act(() => result.current.setPage(2));
    await waitFor(() => expect(result.current.candidates.map((c) => c.id)).toEqual([1, 2]));
  });
});

// ---------------------------------------------------------------------------
// Explicit keep-side
// ---------------------------------------------------------------------------

describe('a and b merge a chosen side rather than the recommended one', () => {
  it('keeps A on a and B on b, overriding the recommendation', async () => {
    // The pair is deliberately one where the recommendation points at B, so a
    // test that merely echoed keepIdForMerge would pass on `a` by accident.
    mockList([
      makeCandidate(1, {
        book_a: { id: 'a1', title: 'Thin' },
        book_b: { id: 'b1', title: 'Rich', asin: 'B00ABC1234' },
      } as Partial<api.DedupCandidate>),
    ]);
    vi.mocked(api.mergeDedupCandidate).mockReturnValue(new Promise<void>(() => {}));
    await renderLane();

    press('a');
    expect(api.mergeDedupCandidate).toHaveBeenCalledWith(1, 'a1');

    vi.mocked(api.mergeDedupCandidate).mockClear();
    mockList([
      makeCandidate(2, {
        book_a: { id: 'a2', title: 'Rich', asin: 'B00ABC1234' },
        book_b: { id: 'b2', title: 'Thin' },
      } as Partial<api.DedupCandidate>),
    ]);
    const second = await renderLane();
    expect(second.result.current.candidates).toHaveLength(1);

    press('b');
    expect(api.mergeDedupCandidate).toHaveBeenCalledWith(2, 'b2');
  });

  it('does not collide with Shift+A select-all', async () => {
    // Shift+A arrives as key 'A'; plain keep-A arrives as 'a'. If the two were
    // folded together, select-all would merge a pair.
    mockList([makeCandidate(1), makeCandidate(2)]);
    const { result } = await renderLane();

    press('A', { shiftKey: true });

    expect(api.mergeDedupCandidate).not.toHaveBeenCalled();
    expect(result.current.selectedIds.size).toBe(2);
  });

  it('leaves an already-decided row alone', async () => {
    mockList([makeCandidate(1, { status: 'merged' })]);
    await renderLane();

    press('a');
    press('b');

    expect(api.mergeDedupCandidate).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// The help overlay and the handler must agree
// ---------------------------------------------------------------------------

describe('every implemented shortcut is documented', () => {
  it('lists each key this suite exercises', () => {
    // A shortcut that exists but is undocumented is a shortcut nobody uses, and
    // the help overlay is the only place the reviewer can learn these. This is
    // asserted against the keys the tests above actually drive, so adding a
    // shortcut without a help entry fails here rather than shipping silently.
    const exercised = ['j / k', 'm', 'a', 'b', 'd', 's', 'Enter', 'Esc', 'Shift+A', '?'];
    const documented = DEDUP_SHORTCUTS.map((s) => s.keys);
    expect(documented).toEqual(exercised);
  });

  it('does not claim m always keeps the recommendation', () => {
    // keepIdForMerge falls back to A on a tie, where recommendedKeepSide
    // returns null and the chip renders nothing. Help text saying "the
    // recommended side" without that caveat would describe a recommendation the
    // reviewer cannot see on exactly the pairs where they need it.
    const m = DEDUP_SHORTCUTS.find((s) => s.keys === 'm');
    expect(m?.action).toMatch(/tie/i);
  });
});

// ---------------------------------------------------------------------------
// Selection must not outlive the rows it points at
// ---------------------------------------------------------------------------

describe('a page change drops the selection', () => {
  it('clears selectedIds when the page changes', async () => {
    // The hazard this closes: selection is keyed by candidate id, so before this
    // it SURVIVED a page turn. Select on page 1, page to 2, press "Merge
    // Selected" and you merge pairs that are not on screen -- and a merge on
    // this lane has no undo.
    const { result } = await renderLane();

    act(() => result.current.toggleSelect(1, 0));
    act(() => result.current.toggleSelect(2, 1));
    expect(result.current.selectedIds.size).toBe(2);

    act(() => result.current.setPage(2));

    expect(result.current.selectedIds.size).toBe(0);
  });

  it('clears selectedIds when the page size changes', async () => {
    const { result } = await renderLane();

    act(() => result.current.toggleSelect(1, 0));
    expect(result.current.selectedIds.size).toBe(1);

    act(() => result.current.setPageSize(100));

    expect(result.current.selectedIds.size).toBe(0);
  });

  it('tells the reviewer the selection was dropped, but only when there was one', async () => {
    // A silent clear trades one hazard for another: the reviewer selects, pages,
    // comes back and finds nothing selected with no explanation. A toast on
    // EVERY page turn would be noise, so it is raised only when rows were armed.
    const { result } = await renderLane();

    act(() => result.current.setPage(2));
    expect(toast).not.toHaveBeenCalled();

    act(() => result.current.toggleSelect(1, 0));
    act(() => result.current.setPage(3));

    expect(toast).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith(expect.stringMatching(/selection cleared/i), 'info');
  });

  it('resets the shift-click anchor so a range cannot span two pages', async () => {
    // The anchor is an INDEX into the visible rows, not an id. Carried across a
    // page turn, the first shift-click on the new page extends from whatever row
    // now sits at that index -- selecting a span the reviewer never pointed at,
    // and feeding it to the same irreversible merge.
    mockList([makeCandidate(1), makeCandidate(2), makeCandidate(3), makeCandidate(4)]);
    const { result } = await renderLane();

    // Anchor at index 0 on the first page.
    act(() => result.current.toggleSelect(1, 0));
    act(() => result.current.setPage(2));

    // With a stale anchor this shift-click would extend 0..2 and select three
    // rows. With the anchor cleared it can only toggle the row clicked.
    act(() => result.current.toggleSelect(3, 2, true));

    expect([...result.current.selectedIds]).toEqual([3]);
  });
});
