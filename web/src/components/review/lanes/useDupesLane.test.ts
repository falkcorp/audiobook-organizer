// file: web/src/components/review/lanes/useDupesLane.test.ts
// version: 1.0.0
// guid: 4a71c8e2-53d9-4f06-b18a-9e2c7d4a0f53
// last-edited: 2026-08-20
//
// The behaviour under test is mostly the behaviour that a port loses silently:
// eight keyboard shortcuts, a suppression guard, a keep-side decision shared
// with the view, and one refusal that prevents an irreversible bulk merge from
// exceeding the filter on screen. None of it fails visibly when dropped.

import { renderHook, act, waitFor } from '@testing-library/react';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../../services/api';
import { useDupesLane, MERGE_ALL_BLOCKED_REASON } from './useDupesLane';

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

async function renderLane() {
  const view = renderHook(() => useDupesLane(toast));
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
    const { result } = await renderLane();
    act(() => result.current.setFilters({ band: 'REVIEW', entityId: 'book-7' }));
    await waitFor(() => expect(result.current.filters.band).toBe('REVIEW'));

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

    act(() => result.current.setFilters({ band: 'CERTAIN' }));
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

    act(() => result.current.setFilters({ band: 'HIGH' }));
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
    const { result } = await renderLane();

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

  it('selects with s and the whole page with Shift+A', async () => {
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
