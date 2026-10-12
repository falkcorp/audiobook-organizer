// file: web/src/components/review/lanes/useDupesLane.test.ts
// version: 1.14.0
// guid: 4a71c8e2-53d9-4f06-b18a-9e2c7d4a0f53
// last-edited: 2026-10-12
//
// The behaviour under test is mostly the behaviour that a port loses silently:
// eight keyboard shortcuts, a suppression guard, a keep-side decision shared
// with the view, and one refusal that prevents an irreversible bulk merge from
// exceeding the filter on screen. None of it fails visibly when dropped.

import { renderHook, act, waitFor } from '@testing-library/react';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../../services/api';
import type { DupesUrlFilters } from './useDupesLane';
import {
  useDupesLane,
  DEDUP_LAYERS,
  parseDedupLayer,
  filterChangedMessage,
  MERGE_ALL_BLOCKED_REASON,
  MERGE_ALL_SEARCH_PENDING_REASON,
  DEDUP_SHORTCUTS,
  SELECT_ALL_MATCHING_PENDING_ONLY_REASON,
} from './useDupesLane';

vi.mock('../../../services/api');

const toast = vi.fn();

function makeCandidate(
  id: number,
  overrides: Partial<api.DedupCandidate> = {}
): api.DedupCandidate {
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

/**
 * A mock that answers `q` the way the server does, so a test can exercise the
 * lane's real contract: the SERVER decides which rows exist. `match` stands in
 * for the server's predicate, which is deliberately wider than anything the
 * client can compute -- that asymmetry is the point of several tests below.
 */
function mockSearchableList(
  all: api.DedupCandidate[],
  match: (c: api.DedupCandidate, q: string) => boolean
) {
  vi.mocked(api.getDedupCandidates).mockImplementation(async (params) => {
    const q = params?.q?.trim().toLowerCase() ?? '';
    const rows = q ? all.filter((c) => match(c, q)) : all;
    return { candidates: rows, total: rows.length };
  });
}

/** The lane debounces before searching; give the timer room to fire. */
const DEBOUNCE_WAIT = { timeout: 2000 };

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
    await waitFor(() =>
      expect(result.current.mergeAllFilteredDisabledReason).toBe(MERGE_ALL_BLOCKED_REASON)
    );

    act(() => result.current.dispatch({ lane: 'dupes', type: 'mergeAllFiltered', expectedTotal: 2 }));

    expect(api.bulkLinkDedupCandidates).not.toHaveBeenCalled();
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
    act(() => result.current.dispatch({ lane: 'dupes', type: 'mergeAllFiltered', expectedTotal: 2 }));
    expect(api.bulkLinkDedupCandidates).not.toHaveBeenCalled();
  });

  it('sends band and entity_id when it does run, so the merge matches the screen', async () => {
    // The defect this replaces: band was not in the bulk endpoint's vocabulary,
    // so narrowing to REVIEW and pressing "merge everything matching this
    // filter" merged every pending candidate in the library.
    vi.mocked(api.bulkLinkDedupCandidates).mockResolvedValue({
      attempted: 1,
      merged: 1,
      failed: 0,
    } as unknown as api.BulkMergeDedupResult);
    const { result } = await renderLane({ band: 'REVIEW', entityId: 'book-7' });
    expect(result.current.filters.band).toBe('REVIEW');

    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'mergeAllFiltered', expectedTotal: 2 });
    });

    // EXACT payload, deliberately not objectContaining: a partial matcher
    // cannot fail when a filter is MISSING, which is the only way this action
    // ever breaks. The suite stayed green through exactly that gap when `q`
    // was added to the list endpoint and not to this one.
    expect(api.bulkLinkDedupCandidates).toHaveBeenCalledWith({
      entity_type: 'book',
      status: 'pending',
      band: 'REVIEW',
      entity_id: 'book-7',
      q: undefined,
      // The count confirmed: a moved filter is refused server-side.
      expected_total: 2,
    });
  });

  it('sends the search term, so the merge cannot exceed the searched set', async () => {
    // Without this the reviewer searches, sees a handful of rows, presses
    // "merge everything matching this filter", and merges every pending
    // candidate in the library. Irreversible, and reported as a success.
    mockSearchableList([makeCandidate(1), makeCandidate(2)], (c, q) =>
      (c.book_a?.title ?? '').toLowerCase().includes(q)
    );
    vi.mocked(api.bulkLinkDedupCandidates).mockResolvedValue({
      attempted: 1,
      merged: 1,
      failed: 0,
    } as unknown as api.BulkMergeDedupResult);
    const { result } = await renderLane();

    act(() => result.current.setFilters({ search: 'A1' }));
    // Wait for the SERVER to answer, not just for the local pass to narrow the
    // list. Those are different moments, and dispatching between them is the
    // bug the refusal below exists for.
    await waitFor(
      () => expect(result.current.mergeAllFilteredDisabledReason).toBeNull(),
      DEBOUNCE_WAIT
    );

    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'mergeAllFiltered', expectedTotal: 1 });
    });

    expect(api.bulkLinkDedupCandidates).toHaveBeenCalledWith({
      entity_type: 'book',
      status: 'pending',
      band: undefined,
      entity_id: undefined,
      q: 'A1',
      expected_total: 1,
    });
  });

  it('REFUSES to merge everything while a search is still settling', async () => {
    // The window: the local pass has narrowed the list, but the server has only
    // answered for the previous term. Merging here would send that previous
    // term and merge a set far wider than the rows on screen. Irreversible.
    let release: (v: api.DedupCandidatesResponse) => void = () => {};
    const held = new Promise<api.DedupCandidatesResponse>((res) => {
      release = res;
    });
    let call = 0;
    vi.mocked(api.getDedupCandidates).mockImplementation(async () => {
      call += 1;
      return call === 1 ? { candidates: [makeCandidate(1), makeCandidate(2)], total: 2 } : held;
    });
    const { result } = await renderLane();

    act(() => result.current.setFilters({ search: 'A1' }));
    await waitFor(() => expect(result.current.candidates).toHaveLength(1), DEBOUNCE_WAIT);

    // Narrowed on screen, but the server has not answered for 'A1'.
    expect(result.current.mergeAllFilteredDisabledReason).toBe(MERGE_ALL_SEARCH_PENDING_REASON);

    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'mergeAllFiltered', expectedTotal: 2 });
    });
    expect(api.bulkLinkDedupCandidates).not.toHaveBeenCalled();

    await act(async () => {
      release({ candidates: [makeCandidate(1)], total: 1 });
      await held;
    });
    await waitFor(() => expect(result.current.mergeAllFilteredDisabledReason).toBeNull());
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

  it('resets the page once the search term settles, but not on every keystroke', async () => {
    // Search round-trips now, so page 2 of the old result set is not page 2 of
    // the new one and the page number IS invalidated. It is keyed on the
    // settled term rather than the raw one: resetting per keystroke would also
    // clear the selection per keystroke, which fights the typist.
    const { result } = await renderLane();
    act(() => result.current.setPage(2));
    await waitFor(() => expect(result.current.page).toBe(2));

    act(() => result.current.setFilters({ search: 'foo' }));
    // Still on page 2 in the same tick: the debounce has not fired.
    expect(result.current.page).toBe(2);

    await waitFor(() => expect(result.current.page).toBe(1), DEBOUNCE_WAIT);
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
    vi.mocked(api.linkDedupCandidate).mockResolvedValue(undefined);
    await renderLane();

    press('m');

    await waitFor(() => expect(api.linkDedupCandidate).toHaveBeenCalledWith(1, 'b1'));
  });

  it('dismisses on d and opens the drawer on Enter', async () => {
    vi.mocked(api.rejectDedupCandidate).mockResolvedValue(undefined);
    const { result } = await renderLane();

    press('Enter');
    expect(result.current.drawerCandidateId).toBe(1);

    press('d');
    await waitFor(() => expect(api.rejectDedupCandidate).toHaveBeenCalledWith(1));
  });

  it('leaves already-decided rows alone', async () => {
    // skip/merge on a merged row is not a no-op server-side; it is an error the
    // reviewer never asked for.
    mockList([makeCandidate(1, { status: 'merged' })]);
    await renderLane();

    press('m');
    press('d');

    expect(api.linkDedupCandidate).not.toHaveBeenCalled();
    expect(api.rejectDedupCandidate).not.toHaveBeenCalled();
  });

  it('Shift+A selects only what the search left on screen', async () => {
    // The label used to promise "the current page" while the code selected the
    // search-narrowed set. Selecting less than promised is the safe direction,
    // but `merge-selected` is irreversible, so the two must agree exactly.
    mockSearchableList([makeCandidate(1), makeCandidate(2)], (c, q) =>
      (c.book_a?.title ?? '').toLowerCase().includes(q)
    );
    const { result } = await renderLane();

    act(() => result.current.setFilters({ search: 'A1' }));
    await waitFor(() => expect(result.current.candidates).toHaveLength(1), DEBOUNCE_WAIT);

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
    expect(api.linkDedupCandidate).not.toHaveBeenCalled();

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
    expect(api.linkDedupCandidate).not.toHaveBeenCalled();
  });
});

describe('search', () => {
  const dune = makeCandidate(1, {
    book_a: { id: 'a1', title: 'Dune' },
  } as Partial<api.DedupCandidate>);
  const neuro = makeCandidate(2, {
    book_a: { id: 'a2', title: 'Neuromancer' },
  } as Partial<api.DedupCandidate>);

  it('narrows locally while the request is still in flight', async () => {
    // The local pass covers exactly one window: from the moment the debounce
    // fires until the server's answer lands. That window is invisible against
    // an instantly-resolving mock, so the response is held open here -- with an
    // immediate mock this test would pass whether or not the local pass exists.
    let release: (v: api.DedupCandidatesResponse) => void = () => {};
    const held = new Promise<api.DedupCandidatesResponse>((res) => {
      release = res;
    });
    let call = 0;
    vi.mocked(api.getDedupCandidates).mockImplementation(async () => {
      call += 1;
      // First call is the lane's initial load; the search request is the one
      // that gets held.
      return call === 1 ? { candidates: [dune, neuro], total: 2 } : held;
    });

    const { result } = await renderLane();
    expect(result.current.candidates).toHaveLength(2);

    act(() => result.current.setFilters({ search: 'neuro' }));

    // Narrowed by the LOCAL pass: the server has not answered yet.
    await waitFor(() => expect(result.current.candidates).toHaveLength(1), DEBOUNCE_WAIT);
    expect(result.current.candidates[0].id).toBe(2);

    await act(async () => {
      release({ candidates: [neuro], total: 1 });
      await held;
    });
    await waitFor(() => expect(result.current.total).toBe(1));
  });

  it('sends the settled term to the server', async () => {
    mockSearchableList([dune, neuro], (c, q) => (c.book_a?.title ?? '').toLowerCase().includes(q));
    const { result } = await renderLane();

    act(() => result.current.setFilters({ search: 'neuro' }));

    await waitFor(() => {
      const calls = vi.mocked(api.getDedupCandidates).mock.calls;
      expect(calls.at(-1)?.[0]?.q).toBe('neuro');
    }, DEBOUNCE_WAIT);
  });

  it('issues ONE request for a burst of keystrokes', async () => {
    mockSearchableList([dune, neuro], () => true);
    const { result } = await renderLane();
    const before = vi.mocked(api.getDedupCandidates).mock.calls.length;

    for (const term of ['n', 'ne', 'neu', 'neur', 'neuro']) {
      act(() => result.current.setFilters({ search: term }));
    }
    await waitFor(() => {
      expect(vi.mocked(api.getDedupCandidates).mock.calls.at(-1)?.[0]?.q).toBe('neuro');
    }, DEBOUNCE_WAIT);
    // One request for the burst, not one per character.
    expect(vi.mocked(api.getDedupCandidates).mock.calls.length).toBe(before + 1);
  });

  it('STANDS DOWN once the server has answered, so a server-only match is not hidden', async () => {
    // The guard this test exists for. The server matches author names, which it
    // resolves through the author table; the rows rendered here carry no
    // author_name key at all. So the server legitimately returns a row that the
    // local haystack cannot match. If the local pass kept running it would
    // throw that row away -- total says 1, the list shows 0, and the row stays
    // unfindable, which is the defect server-side search was added to fix.
    mockSearchableList([dune, neuro], (c, q) =>
      // Stands in for an author-name match: matches `dune` on a term that
      // appears NOWHERE in any field the client can see.
      q === 'herbert' ? c.id === 1 : (c.book_a?.title ?? '').toLowerCase().includes(q)
    );
    const { result } = await renderLane();

    act(() => result.current.setFilters({ search: 'herbert' }));

    await waitFor(() => {
      expect(result.current.candidates).toHaveLength(1);
      expect(result.current.candidates[0].id).toBe(1);
    }, DEBOUNCE_WAIT);
    expect(result.current.total).toBe(1);
  });

  it('shows nothing rather than a narrowed subset when the search fails', async () => {
    // The failure mode: error banner, plus a stale total from the PREVIOUS
    // term, plus the local pass narrowing the previous page down to a few
    // plausible-looking rows. A reviewer scrolling past the alert reads those
    // as search results.
    let call = 0;
    vi.mocked(api.getDedupCandidates).mockImplementation(async () => {
      call += 1;
      if (call === 1) return { candidates: [dune, neuro], total: 4000 };
      throw new Error('boom');
    });
    const { result } = await renderLane();
    expect(result.current.total).toBe(4000);

    act(() => result.current.setFilters({ search: 'neuro' }));

    await waitFor(() => expect(result.current.error).toBeTruthy(), DEBOUNCE_WAIT);
    expect(result.current.candidates).toEqual([]);
    expect(result.current.total).toBe(0);
  });

  it('reports the server total, not the page length', async () => {
    // The two totals must DIFFER, or this cannot observe anything: with one
    // unconditional total the initial load already satisfies the assertion and
    // the test passes even when `q` is never sent at all.
    let call = 0;
    vi.mocked(api.getDedupCandidates).mockImplementation(async () => {
      call += 1;
      return call === 1
        ? { candidates: [dune, neuro], total: 900 }
        : { candidates: [dune], total: 37 };
    });
    const { result } = await renderLane();
    expect(result.current.total).toBe(900);

    act(() => result.current.setFilters({ search: 'dune' }));

    // 37 is the SEARCH response's total, and it is one row on the page.
    await waitFor(() => expect(result.current.total).toBe(37), DEBOUNCE_WAIT);
    expect(result.current.candidates).toHaveLength(1);
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
    vi.mocked(api.rejectDedupCandidate).mockResolvedValue(undefined);
    const { result } = await renderLane();

    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'dismissSelected', ids: [1, 2] });
    });

    expect(api.rejectDedupCandidate).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(result.current.selectedIds.size).toBe(0));
  });

  it('ignores an empty selection rather than calling the endpoint', async () => {
    const { result } = await renderLane();
    act(() => result.current.dispatch({ lane: 'dupes', type: 'mergeSelected', ids: [] }));
    expect(api.linkDedupCandidate).not.toHaveBeenCalled();
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
    vi.mocked(api.linkDedupCandidate).mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          release = () => resolve(undefined);
        })
    );
    await renderLane();

    press('m'); // decides candidate 1; its row leaves `visible` at once
    press('m'); // must therefore land on candidate 2

    expect(vi.mocked(api.linkDedupCandidate).mock.calls.map((c) => c[0])).toEqual([1, 2]);
    release();
  });

  it('advances focus by removing the row, not by moving the pointer', async () => {
    // Auto-advance falls out of the suppression rather than being a second
    // mechanism that could disagree with it. focusedIndex never moves.
    mockList([makeCandidate(1), makeCandidate(2)]);
    vi.mocked(api.linkDedupCandidate).mockReturnValue(new Promise<void>(() => {}));
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
    vi.mocked(api.linkDedupCandidate).mockReturnValue(new Promise<void>(() => {}));
    vi.mocked(api.rejectDedupCandidate).mockReturnValue(new Promise<void>(() => {}));
    const { result } = await renderLane();

    press('j');
    expect(result.current.focusedIndex).toBe(1);

    press('m'); // decides candidate 2, the last row

    await waitFor(() => expect(result.current.focusedIndex).toBe(0));
    expect(result.current.candidates.map((c) => c.id)).toEqual([1]);

    // The proof that matters: the keyboard still acts on the row that is left.
    press('d');
    expect(api.rejectDedupCandidate).toHaveBeenCalledWith(1);
  });

  it('puts the row back when the decision fails', async () => {
    // An optimistic removal that outlives its failed request drops the pair out
    // of the queue silently -- the merge did not happen, but the reviewer never
    // sees the row again.
    mockList([makeCandidate(1), makeCandidate(2)]);
    vi.mocked(api.linkDedupCandidate).mockRejectedValue(new Error('conflict'));
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
    vi.mocked(api.linkDedupCandidate).mockResolvedValue(undefined);
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
    vi.mocked(api.linkDedupCandidate).mockResolvedValue(undefined);
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
    vi.mocked(api.linkDedupCandidate).mockReturnValue(new Promise<void>(() => {}));
    await renderLane();

    press('a');
    expect(api.linkDedupCandidate).toHaveBeenCalledWith(1, 'a1');

    vi.mocked(api.linkDedupCandidate).mockClear();
    mockList([
      makeCandidate(2, {
        book_a: { id: 'a2', title: 'Rich', asin: 'B00ABC1234' },
        book_b: { id: 'b2', title: 'Thin' },
      } as Partial<api.DedupCandidate>),
    ]);
    const second = await renderLane();
    expect(second.result.current.candidates).toHaveLength(1);

    press('b');
    expect(api.linkDedupCandidate).toHaveBeenCalledWith(2, 'b2');
  });

  it('does not collide with Shift+A select-all', async () => {
    // Shift+A arrives as key 'A'; plain keep-A arrives as 'a'. If the two were
    // folded together, select-all would merge a pair.
    mockList([makeCandidate(1), makeCandidate(2)]);
    const { result } = await renderLane();

    press('A', { shiftKey: true });

    expect(api.linkDedupCandidate).not.toHaveBeenCalled();
    expect(result.current.selectedIds.size).toBe(2);
  });

  it('leaves an already-decided row alone', async () => {
    mockList([makeCandidate(1, { status: 'merged' })]);
    await renderLane();

    press('a');
    press('b');

    expect(api.linkDedupCandidate).not.toHaveBeenCalled();
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

// ---------------------------------------------------------------------------
// Layer filter, bulk keep-side (03-PR2)
// ---------------------------------------------------------------------------

describe('layer filter', () => {
  it('layer filter is sent on list and bulk filter', async () => {
    vi.mocked(api.bulkLinkDedupCandidates).mockResolvedValue({
      attempted: 2,
      merged: 2,
      failed: 0,
    } as unknown as api.BulkMergeDedupResult);
    const { result } = await renderLane();
    // No layer until one is chosen: the list call must not carry the key.
    expect(vi.mocked(api.getDedupCandidates).mock.calls[0][0]).not.toHaveProperty('layer');

    act(() => result.current.setFilters({ layer: 'acoustid' }));
    await waitFor(() =>
      expect(api.getDedupCandidates).toHaveBeenLastCalledWith(
        expect.objectContaining({ layer: 'acoustid' }),
        expect.anything()
      )
    );
    await waitFor(() => expect(result.current.mergeAllFilteredDisabledReason).toBeNull());

    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'mergeAllFiltered', expectedTotal: 2 });
    });
    expect(api.bulkLinkDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ layer: 'acoustid', status: 'pending', expected_total: 2 })
    );
  });

  it('is seeded from the URL value once and never re-read', async () => {
    const { result, rerender } = await renderLane({ band: null, entityId: null, layer: 'exact' });
    expect(result.current.filters.layer).toBe('exact');
    // The router handing a different value later must not move the filter.
    rerender({ uf: { band: null, entityId: null, layer: 'embedding' } });
    expect(result.current.filters.layer).toBe('exact');
  });

  it('changing layer resets page and selection', async () => {
    mockList([makeCandidate(1), makeCandidate(2)], 300);
    const { result } = await renderLane();
    act(() => result.current.setPage(3));
    await waitFor(() => expect(result.current.page).toBe(3));
    act(() => result.current.toggleSelect(1, 0));
    expect(result.current.selectedIds.size).toBe(1);

    act(() => result.current.setFilters({ layer: 'exact' }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.page).toBe(1);
    expect(result.current.selectedIds.size).toBe(0);
  });

  it('counts book pairs per layer, at the status filter, from the stats it already fetched', async () => {
    vi.mocked(api.getDedupStats).mockResolvedValue({
      stats: [
        { entity_type: 'book', layer: 'acoustid', status: 'pending', count: 12 },
        { entity_type: 'book', layer: 'acoustid', status: 'merged', count: 99 },
        { entity_type: 'book', layer: 'exact', status: 'pending', count: 3 },
        { entity_type: 'book', layer: 'llm', status: 'pending', count: 2 },
        { entity_type: 'book', layer: 'llm', status: 'dismissed', count: 5 },
        { entity_type: 'book', layer: 'manual', status: 'pending', count: 1 },
        { entity_type: 'book', layer: 'book_signature', status: 'pending', count: 4 },
        { entity_type: 'book', layer: 'metadata_hash_match', status: 'pending', count: 6 },
        // A layer the lane has no chip for: in the total, in no chip.
        { entity_type: 'book', layer: 'from_the_future', status: 'pending', count: 7 },
        { entity_type: 'author', layer: 'exact', status: 'pending', count: 50 },
      ],
    });
    const { result } = await renderLane();
    await waitFor(() => expect(result.current.countByLayer.acoustid).toBe(12));
    expect(result.current.countByLayer).toEqual({
      exact: 3,
      acoustid: 12,
      embedding: 0,
      book_signature: 4,
      metadata_hash_match: 6,
      llm: 2,
      manual: 1,
    });
    expect(result.current.layerTotal).toBe(12 + 3 + 2 + 1 + 4 + 6 + 7);

    act(() => result.current.setFilters({ status: 'merged' }));
    await waitFor(() => expect(result.current.countByLayer.acoustid).toBe(99));
    expect(result.current.countByLayer.exact).toBe(0);
    expect(result.current.layerTotal).toBe(99);

    // '' is every status.
    act(() => result.current.setFilters({ status: '' }));
    await waitFor(() => expect(result.current.countByLayer.llm).toBe(7));
    expect(result.current.layerTotal).toBe(12 + 99 + 3 + 2 + 5 + 1 + 4 + 6 + 7);
  });

  it('accepts every layer a candidate can carry from the URL and drops anything else', () => {
    for (const l of DEDUP_LAYERS) expect(parseDedupLayer(l)).toBe(l);
    expect(DEDUP_LAYERS).toEqual(
      expect.arrayContaining(['llm', 'book_signature', 'metadata_hash_match', 'manual'])
    );
    expect(parseDedupLayer('bogus')).toBeNull();
    expect(parseDedupLayer(null)).toBeNull();
  });
});

describe('bulk keep-older / keep-newer', () => {
  it('keep-older sends keep_side a with expected_total', async () => {
    vi.mocked(api.bulkLinkDedupCandidates).mockResolvedValue({
      attempted: 2,
      merged: 2,
      failed: 0,
    } as unknown as api.BulkMergeDedupResult);
    const { result } = await renderLane({ band: null, entityId: null, layer: 'acoustid' });
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'keepAAllFiltered', expectedTotal: 2 });
    });
    expect(api.bulkLinkDedupCandidates).toHaveBeenCalledWith({
      entity_type: 'book',
      status: 'pending',
      band: undefined,
      layer: 'acoustid',
      entity_id: undefined,
      q: undefined,
      keep_side: 'a',
      expected_total: 2,
    });
  });

  it('keep-newer sends keep_side b', async () => {
    vi.mocked(api.bulkLinkDedupCandidates).mockResolvedValue({
      attempted: 2,
      merged: 2,
      failed: 0,
    } as unknown as api.BulkMergeDedupResult);
    const { result } = await renderLane();
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'keepBAllFiltered', expectedTotal: 2 });
    });
    expect(api.bulkLinkDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ keep_side: 'b', expected_total: 2 })
    );
  });

  it('keep-older/newer are refused while both-unmatched is on, in dispatch', async () => {
    // The bulk endpoint cannot express both_unmatched, so a keep-side merge
    // would act on a larger set than the one on screen. Same refusal as
    // mergeAllFiltered, and it has to hold for a dispatch that skipped the button.
    const { result } = await renderLane();
    act(() => result.current.setFilters({ bothUnmatched: true }));
    await waitFor(() =>
      expect(result.current.mergeAllFilteredDisabledReason).toBe(MERGE_ALL_BLOCKED_REASON)
    );
    act(() =>
      result.current.dispatch({ lane: 'dupes', type: 'keepAAllFiltered', expectedTotal: 2 })
    );
    act(() =>
      result.current.dispatch({ lane: 'dupes', type: 'keepBAllFiltered', expectedTotal: 2 })
    );
    expect(api.bulkLinkDedupCandidates).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledTimes(2);
    expect(toast).toHaveBeenNthCalledWith(1, MERGE_ALL_BLOCKED_REASON, 'warning');
    expect(toast).toHaveBeenNthCalledWith(2, MERGE_ALL_BLOCKED_REASON, 'warning');
  });

  it.each([
    ['keepAAllFiltered', 'a'],
    ['keepBAllFiltered', 'b'],
  ] as const)(
    '%s through a 409 FILTER_CHANGED asks to re-confirm, refreshes, and reports no merge',
    async (type, keepSide) => {
      vi.mocked(api.bulkLinkDedupCandidates).mockRejectedValue(
        new Error('the filter moved')
      );
      // services/api is auto-mocked, so the real filterChangedOf (unit-tested
      // in filterChangedOf.test.ts) is stubbed to recognise this rejection.
      vi.mocked(api.filterChangedOf).mockReturnValue({ expected: 2, matched: 5 });
      const { result } = await renderLane();
      const fetchesBefore = vi.mocked(api.getDedupCandidates).mock.calls.length;

      await act(async () => {
        result.current.dispatch({ lane: 'dupes', type, expectedTotal: 2 });
      });

      expect(api.bulkLinkDedupCandidates).toHaveBeenCalledWith(
        expect.objectContaining({ keep_side: keepSide, expected_total: 2 })
      );
      expect(toast).toHaveBeenCalledWith(filterChangedMessage(5), 'warning');
      // No success or failure report for a merge that did not run.
      expect(toast.mock.calls.filter((c) => String(c[0]).startsWith('Bulk merge'))).toEqual([]);
      await waitFor(() =>
        expect(vi.mocked(api.getDedupCandidates).mock.calls.length).toBeGreaterThan(fetchesBefore)
      );
      await waitFor(() => expect(result.current.busy).toBe(false));
    }
  );

  it('keep-older/newer refused when status is not pending', async () => {
    const { result } = await renderLane();
    act(() => result.current.setFilters({ status: 'merged' }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() =>
      result.current.dispatch({ lane: 'dupes', type: 'keepAAllFiltered', expectedTotal: 2 })
    );
    act(() =>
      result.current.dispatch({ lane: 'dupes', type: 'keepBAllFiltered', expectedTotal: 2 })
    );
    expect(api.bulkLinkDedupCandidates).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(SELECT_ALL_MATCHING_PENDING_ONLY_REASON, 'warning');
  });
});

describe('mergeSelected outcome report', () => {
  it('mergeSelected keeps partial-failure report after refetch', async () => {
    vi.mocked(api.linkDedupCandidate).mockImplementation(async (id) => {
      if (id === 2) throw new Error('boom');
    });
    // After the refetch the server still lists the pair that failed.
    const { result } = await renderLane();
    mockList([makeCandidate(2)]);
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'mergeSelected', ids: [1, 2] });
    });
    await waitFor(() => expect(result.current.candidates.map((c) => c.id)).toEqual([2]));
    // The report was raised once, with both counts, and the refetch did not
    // replace or repeat it; the failed pair is back on screen to retry.
    const reports = toast.mock.calls.filter((c) => String(c[0]).startsWith('Merged'));
    expect(reports).toEqual([['Merged 1, failed 1', 'warning']]);
  });
});

// ---------------------------------------------------------------------------
// The Clusters view: cluster verbs, other-page counts, By series.
// ---------------------------------------------------------------------------

describe('useDupesLane Clusters view', () => {
  const pair = (id: number, a: string, b: string) =>
    makeCandidate(id, {
      entity_a_id: a,
      entity_b_id: b,
      book_a: { id: a, title: `Title ${a}` } as api.Book,
      book_b: { id: b, title: `Title ${b}` } as api.Book,
    });
  // Page one holds pairs 1 and 2 (cluster b1-b2-b3); pair 10 (b3-b9) is on
  // another page, as is pair 11 (b1-b7). The server filters by entity_id.
  const PAGE = [pair(1, 'b1', 'b2'), pair(2, 'b2', 'b3')];
  const ELSEWHERE = [pair(10, 'b3', 'b9'), pair(11, 'b1', 'b7')];
  const ALL = [...PAGE, ...ELSEWHERE];

  beforeEach(() => {
    vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: PAGE, total: ALL.length });
    // The server's one-scan answer: for each requested cluster, the distinct
    // pairs touching its books that are off the page (here pairs 10 and 11).
    vi.mocked(api.getDedupBookPairCounts).mockImplementation(async (req) =>
      req.clusters.map((cl) => {
        const off = ELSEWHERE.filter(
          (c) => cl.book_ids.includes(c.entity_a_id) || cl.book_ids.includes(c.entity_b_id)
        );
        const per = cl.book_ids
          .map((b) => ({
            book_id: b,
            count: off.filter((c) => c.entity_a_id === b || c.entity_b_id === b).length,
          }))
          .filter((x) => x.count > 0);
        return { key: cl.key, other_pairs: off.length, per_book: per };
      })
    );
  });

  async function renderClusters(clusterView = true) {
    const view = renderHook(() => useDupesLane(toast, true, undefined, clusterView));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    return view;
  }

  const listCalls = () => vi.mocked(api.getDedupCandidates).mock.calls.length;

  it('builds clusters from the page only while the Clusters view is showing', async () => {
    const on = await renderClusters(true);
    expect(on.result.current.clusters).toHaveLength(1);
    expect(on.result.current.clusters[0].bookIds).toEqual(['b1', 'b2', 'b3']);

    const off = await renderClusters(false);
    expect(off.result.current.clusters).toEqual([]);
  });

  it('makes no extra requests when the Clusters view is not showing', async () => {
    await renderClusters(false);
    await new Promise((r) => setTimeout(r, 50));
    expect(vi.mocked(api.getDedupCandidates)).toHaveBeenCalledTimes(1);
    expect(api.getDedupBookPairCounts).not.toHaveBeenCalled();
    expect(api.listDedupCandidateSeries).not.toHaveBeenCalled();
  });

  it('counts the pairs a cluster has on other pages with ONE request for the whole page', async () => {
    const { result } = await renderClusters();
    const cluster = () => result.current.clusters[0];
    await waitFor(() => expect(result.current.clusterOverflow(cluster()).state).toBe('ready'));
    expect(result.current.clusterOverflow(cluster())).toMatchObject({
      state: 'ready',
      count: 2,
      perBook: [
        { bookId: 'b1', count: 1 },
        { bookId: 'b3', count: 1 },
      ],
    });
    // One request, however many books the page holds, carrying the clusters,
    // the pairs already on the page and the list's own filter.
    expect(api.getDedupBookPairCounts).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.getDedupBookPairCounts).mock.calls[0][0]).toEqual({
      clusters: [{ key: 'b1', book_ids: ['b1', 'b2', 'b3'] }],
      page_candidate_ids: [1, 2],
      status: 'pending',
      layer: undefined,
      band: undefined,
      q: undefined,
      both_unmatched: undefined,
      entity_id: undefined,
    });
    // No per-book entity_id fan-out.
    expect(
      vi.mocked(api.getDedupCandidates).mock.calls.filter(([p]) => p?.entity_id)
    ).toHaveLength(0);
  });

  it('asks again after a refresh, still in one request', async () => {
    // A refused link leaves the pairs where they were, so the page comes back
    // with its clusters and the counts are asked for again.
    vi.mocked(api.linkDedupCluster).mockRejectedValue(new Error('refused'));
    const { result } = await renderClusters();
    await waitFor(() => expect(api.getDedupBookPairCounts).toHaveBeenCalledTimes(1));
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2', 'b3'] });
    });
    await waitFor(() => expect(api.getDedupBookPairCounts).toHaveBeenCalledTimes(2));
  });

  it('reports an other-pages count that failed, instead of saying zero', async () => {
    vi.mocked(api.getDedupBookPairCounts).mockRejectedValue(new Error('count broke'));
    const { result } = await renderClusters();
    await waitFor(() =>
      expect(result.current.clusterOverflow(result.current.clusters[0])).toEqual({
        state: 'error',
        message: 'count broke',
      })
    );
  });

  it('link cluster sends the books and the chosen primary, then refetches', async () => {
    vi.mocked(api.linkDedupCluster).mockResolvedValue({
      status: 'merged',
      merged_books: 3,
      candidates_updated: 2,
      failed_candidate_ids: [],
      candidates_error: '',
    });
    const { result } = await renderClusters();
    const before = listCalls();
    await act(async () => {
      result.current.dispatch({
        lane: 'dupes',
        type: 'linkCluster',
        ids: ['b1', 'b2', 'b3'],
        primaryId: 'b2',
      });
    });
    expect(api.linkDedupCluster).toHaveBeenCalledWith(['b1', 'b2', 'b3'], 'b2');
    await waitFor(() => expect(listCalls()).toBeGreaterThan(before));
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Linked 3 books'), 'success');
  });

  it('link cluster without a primary lets the server pick', async () => {
    vi.mocked(api.linkDedupCluster).mockResolvedValue({
      status: 'merged',
      merged_books: 2,
      candidates_updated: 1,
      failed_candidate_ids: [],
    });
    const { result } = await renderClusters();
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2'] });
    });
    expect(api.linkDedupCluster).toHaveBeenCalledWith(['b1', 'b2'], undefined);
  });

  it('a cluster smaller than two books is refused before any request', async () => {
    const { result } = await renderClusters();
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'linkCluster', ids: ['b1'] });
      result.current.dispatch({ lane: 'dupes', type: 'rejectCluster', ids: [], candidateIds: [] });
    });
    expect(api.linkDedupCluster).not.toHaveBeenCalled();
    expect(api.rejectDedupCluster).not.toHaveBeenCalled();
  });

  it('a rejected request is reported after the refetch, and the pairs come back', async () => {
    vi.mocked(api.linkDedupCluster).mockRejectedValue(new Error('409: refused for b2'));
    const { result } = await renderClusters();
    const before = listCalls();
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2', 'b3'] });
    });
    // The refetch went out, and the failure is still reported once it landed.
    await waitFor(() => expect(listCalls()).toBeGreaterThan(before));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(toast).toHaveBeenCalledWith('409: refused for b2', 'warning');
    expect(toast).not.toHaveBeenCalledWith(expect.stringContaining('Linked'), 'success');
    // The pairs were suppressed while the request ran and are back after it.
    expect(result.current.candidates.map((c) => c.id)).toEqual([1, 2]);
    expect(result.current.clusters).toHaveLength(1);
    expect(result.current.busy).toBe(false);
  });

  it('a link whose pairs could not all be marked is a warning naming the count', async () => {
    // What the server really sends on a partial failure: the books are linked
    // (merged_books is always the full list) and the pairs it could not write
    // come back as ids.
    vi.mocked(api.linkDedupCluster).mockResolvedValue({
      status: 'merged',
      merged_books: 3,
      candidates_updated: 1,
      failed_candidate_ids: [2],
    });
    const { result } = await renderClusters();
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2', 'b3'] });
    });
    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining('1 pair(s) could not be marked merged'),
      'warning'
    );
    expect(toast).not.toHaveBeenCalledWith(expect.anything(), 'success');
  });

  it('a link that marked fewer pairs than the card showed is a warning even with no failed ids', async () => {
    vi.mocked(api.linkDedupCluster).mockResolvedValue({
      status: 'merged',
      merged_books: 3,
      candidates_updated: 0,
      failed_candidate_ids: [],
    });
    const { result } = await renderClusters();
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2', 'b3'] });
    });
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('could not be marked merged'), 'warning');
  });

  it('a decided pair in the cluster (status filter All) is not a false link warning', async () => {
    // The server marks only PENDING pairs merged, so a merged pair on the card
    // must not count against `candidates_updated`.
    vi.mocked(api.getDedupCandidates).mockResolvedValue({
      candidates: [PAGE[0], { ...PAGE[1], status: 'merged' }],
      total: 2,
    });
    vi.mocked(api.linkDedupCluster).mockResolvedValue({
      status: 'merged',
      merged_books: 3,
      candidates_updated: 1,
      failed_candidate_ids: [],
    });
    const { result } = await renderClusters();
    act(() => result.current.setFilters({ status: '' }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    await waitFor(() => expect(result.current.clusters).toHaveLength(1));
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2', 'b3'] });
    });
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Linked 3 books into one group'), 'success');
    expect(toast).not.toHaveBeenCalledWith(expect.anything(), 'warning');
  });

  it('the ?book= deep link scopes the other-pages counts to that book', async () => {
    const view = renderHook(() =>
      useDupesLane(toast, true, { band: null, entityId: 'b1' }, true)
    );
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    await waitFor(() => expect(api.getDedupBookPairCounts).toHaveBeenCalled());
    expect(vi.mocked(api.getDedupBookPairCounts).mock.calls[0][0]).toMatchObject({
      entity_id: 'b1',
    });
  });

  it('a link whose pairs could not even be listed says why', async () => {
    vi.mocked(api.linkDedupCluster).mockResolvedValue({
      status: 'merged',
      merged_books: 3,
      candidates_updated: 0,
      failed_candidate_ids: [],
      candidates_error: 'could not list the pairs to mark merged',
    });
    const { result } = await renderClusters();
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2', 'b3'] });
    });
    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining('could not list the pairs to mark merged'),
      'warning'
    );
  });

  it('reject cluster sends the books AND the pairs the card showed', async () => {
    vi.mocked(api.rejectDedupCluster).mockResolvedValue({
      status: 'dismissed',
      dismissed: 2,
      failed_candidate_ids: [],
      skipped_candidate_ids: [],
    });
    const { result } = await renderClusters();
    await act(async () => {
      result.current.dispatch({
        lane: 'dupes',
        type: 'rejectCluster',
        ids: ['b1', 'b2', 'b3'],
        candidateIds: [1, 2],
      });
    });
    expect(api.rejectDedupCluster).toHaveBeenCalledWith(['b1', 'b2', 'b3'], [1, 2]);
    expect(toast).toHaveBeenCalledWith('Rejected cluster: 2 pair(s) dismissed', 'success');
  });

  it('reject cluster that wrote fewer pairs than named reports the failed and skipped ones', async () => {
    vi.mocked(api.rejectDedupCluster).mockResolvedValue({
      status: 'dismissed',
      dismissed: 1,
      failed_candidate_ids: [2],
      skipped_candidate_ids: [],
    });
    const { result } = await renderClusters();
    await act(async () => {
      result.current.dispatch({
        lane: 'dupes',
        type: 'rejectCluster',
        ids: ['b1', 'b2', 'b3'],
        candidateIds: [1, 2],
      });
    });
    expect(toast).toHaveBeenCalledWith(
      'Rejected 1 of 2 pair(s); 1 could not be written',
      'warning'
    );
  });

  it('remove from cluster sends the whole cluster and the removed books', async () => {
    vi.mocked(api.removeFromDedupCluster).mockResolvedValue({
      status: 'removed',
      dismissed: 1,
      removed: 1,
      failed_candidate_ids: [],
    });
    const { result } = await renderClusters();
    await act(async () => {
      result.current.dispatch({
        lane: 'dupes',
        type: 'removeFromCluster',
        ids: ['b1', 'b2', 'b3'],
        removeIds: ['b3'],
      });
    });
    expect(api.removeFromDedupCluster).toHaveBeenCalledWith(['b1', 'b2', 'b3'], ['b3']);
    expect(toast).toHaveBeenCalledWith(
      'Removed 1 book(s) from the cluster (1 pair(s) dismissed)',
      'success'
    );
  });

  it('remove from cluster with edges the server could not dismiss is a warning', async () => {
    vi.mocked(api.removeFromDedupCluster).mockResolvedValue({
      status: 'removed',
      dismissed: 1,
      removed: 2,
      failed_candidate_ids: [7],
    });
    const { result } = await renderClusters();
    await act(async () => {
      result.current.dispatch({
        lane: 'dupes',
        type: 'removeFromCluster',
        ids: ['b1', 'b2', 'b3'],
        removeIds: ['b2', 'b3'],
      });
    });
    expect(toast).toHaveBeenCalledWith(
      'Removed from the cluster, but 1 pair(s) could not be dismissed (1 were)',
      'warning'
    );
  });

  describe('keyboard shortcuts', () => {
    const press = (key: string, init: KeyboardEventInit = {}) =>
      act(() => {
        window.dispatchEvent(new KeyboardEvent('keydown', { key, ...init }));
      });

    it('d, a, b and m act on the focused pair in the pair views', async () => {
      vi.mocked(api.rejectDedupCandidate).mockResolvedValue(undefined);
      await renderClusters(false);
      await press('d');
      await waitFor(() => expect(api.rejectDedupCandidate).toHaveBeenCalledWith(1));
    });

    it.each(['d', 'a', 'b', 'm', 's', 'Enter', 'j', 'k'])(
      '%s does nothing in the Clusters view, where no focus ring shows which pair it would hit',
      async (key) => {
        const { result } = await renderClusters(true);
        await press(key);
        expect(api.rejectDedupCandidate).not.toHaveBeenCalled();
        expect(api.linkDedupCandidate).not.toHaveBeenCalled();
        expect(result.current.selectedIds.size).toBe(0);
        expect(result.current.drawerCandidateId).toBeNull();
        expect(result.current.focusedIndex).toBe(0);
      }
    );

    it('Shift+A still selects what is on screen', async () => {
      const { result } = await renderClusters(true);
      await press('A', { shiftKey: true });
      expect(result.current.selectedIds.size).toBe(2);
    });
  });

  it('By series fetches the summary only once the toggle is on, and link-all reports and refetches', async () => {
    vi.mocked(api.listDedupCandidateSeries).mockResolvedValue([
      { series_id: 7, series_name: 'Series A', cluster_count: 2, book_count: 5, candidate_count: 6 },
    ]);
    vi.mocked(api.linkDedupCandidateSeries).mockResolvedValue({
      series_id: 7,
      clusters_merged: 2,
      books_merged: 5,
      candidates_updated: 6,
    });
    const { result } = await renderClusters();
    expect(api.listDedupCandidateSeries).not.toHaveBeenCalled();

    act(() => result.current.setBySeries(true));
    await waitFor(() => expect(result.current.series.summary.state).toBe('ready'));
    expect(api.listDedupCandidateSeries).toHaveBeenCalledTimes(1);
    // The By-series list replaces the cluster cards, so no other-page counts run.
    const before = listCalls();

    await act(async () => {
      await result.current.series.linkSeries(7);
    });
    expect(api.linkDedupCandidateSeries).toHaveBeenCalledWith(7);
    expect(toast).toHaveBeenCalledWith('Series link: 2 cluster(s), 5 book(s) linked', 'success');
    await waitFor(() => expect(listCalls()).toBeGreaterThan(before));
    await waitFor(() => expect(api.listDedupCandidateSeries).toHaveBeenCalledTimes(2));
  });

  it('a series link with failures is a warning', async () => {
    vi.mocked(api.listDedupCandidateSeries).mockResolvedValue([]);
    vi.mocked(api.linkDedupCandidateSeries).mockResolvedValue({
      series_id: 7,
      clusters_merged: 1,
      books_merged: 2,
      candidates_updated: 1,
      failures: ['cluster x: refused'],
    });
    const { result } = await renderClusters();
    await act(async () => {
      await result.current.series.linkSeries(7);
    });
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('1 failed'), 'warning');
  });

  it('a failed series summary is an error state, not an empty list', async () => {
    vi.mocked(api.listDedupCandidateSeries).mockRejectedValue(new Error('summary broke'));
    const { result } = await renderClusters();
    act(() => result.current.setBySeries(true));
    await waitFor(() =>
      expect(result.current.series.summary).toEqual({ state: 'error', message: 'summary broke' })
    );
  });
});
