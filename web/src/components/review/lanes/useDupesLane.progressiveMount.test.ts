// file: web/src/components/review/lanes/useDupesLane.progressiveMount.test.ts
// version: 1.0.0
// guid: 3f8b1d64-9e27-4c05-b3a1-7d20e6c85f49
// last-edited: 2026-09-01
//
// Pins the progressive mount added to useDupesLane, and -- more importantly --
// pins the safety property it could have broken.
//
// A 100-row page used to render every row in ONE synchronous task: 156 ms, well
// past the ~50 ms at which a user feels a freeze. The lane now commits a first
// slice synchronously and lifts the cap in a transition.
//
// The hazard that creates is `selectAllVisible`. It exists to guarantee a
// reviewer can never stage rows they cannot see for a merge that cannot be
// undone, and it does that by reading the SAME `visible` the rows are rendered
// from. A mount cap that fed the rows but not select-all would silently break
// exactly that guarantee, and no test in this repo could have observed it
// before this file -- the mount window did not previously exist.
//
// None of these assertions can be satisfied by the pre-change lane: it had no
// intermediate commit at all, so the "first slice" every test here looks for is
// the whole page.

import { renderHook, act, waitFor } from '@testing-library/react';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../../services/api';
import { useDupesLane, FIRST_PAINT_ROWS } from './useDupesLane';

vi.mock('../../../services/api');

const toast = vi.fn();

/** Comfortably more than one slice, and one of the real page-size options. */
const PAGE = 100;

function makeCandidate(id: number): api.DedupCandidate {
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
  } as api.DedupCandidate;
}

const fullPage = Array.from({ length: PAGE }, (_, i) => makeCandidate(i + 1));

beforeEach(() => {
  // This file resets rather than relying on the project default: the repo sets
  // neither clearMocks nor restoreMocks, so `mock.calls` otherwise accumulate
  // across tests in one file and a count assertion reads a previous test's
  // calls. Nothing here asserts on a call count, but the reset keeps it that
  // way for whoever adds the next test.
  vi.resetAllMocks();
  toast.mockClear();
  vi.mocked(api.getDedupCandidates).mockResolvedValue({
    candidates: fullPage,
    total: PAGE,
  });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
});

describe('progressive mount', () => {
  it('commits a bounded first slice before the whole page', async () => {
    // The headline behaviour. Before this change the first commit that had any
    // rows in it had ALL of them, which is the 156 ms task.
    const seen: number[] = [];
    const { result } = renderHook(() => {
      const lane = useDupesLane(toast, true, { band: null, entityId: null });
      seen.push(lane.candidates.length);
      return lane;
    });

    await waitFor(() => expect(result.current.candidates.length).toBe(PAGE));

    const firstNonEmpty = seen.find((n) => n > 0);
    expect(firstNonEmpty).toBe(FIRST_PAINT_ROWS);
    // And it really is an intermediate step, not the end state.
    expect(FIRST_PAINT_ROWS).toBeLessThan(PAGE);
  });

  it('never commits an empty page between the fetch and the rows', async () => {
    // Why the first slice is synchronous rather than the whole list being
    // deferred. A bare useDeferredValue would hand the urgent render the
    // PREVIOUS (empty) list while `loading` was already false, and the spine
    // renders its "no duplicate candidates" empty state when it gets no rows --
    // so the reviewer would see a committed frame claiming there is nothing
    // here, immediately before 100 rows appeared.
    const seen: { rows: number; loading: boolean }[] = [];
    const { result } = renderHook(() => {
      const lane = useDupesLane(toast, true, { band: null, entityId: null });
      seen.push({ rows: lane.candidates.length, loading: lane.loading });
      return lane;
    });

    await waitFor(() => expect(result.current.candidates.length).toBe(PAGE));

    // No commit may claim "done loading, and there is nothing to show" while
    // the page in fact has rows.
    expect(seen.filter((s) => !s.loading && s.rows === 0)).toEqual([]);
  });

  it('select-all cannot reach rows that have not mounted yet', async () => {
    // THE safety property. `selectAllVisible` feeds an irreversible bulk merge,
    // so during the mount window it must select what is on screen -- the first
    // slice -- and not the whole page sitting in memory behind it.
    let capturedSelectAll: (() => void) | null = null;
    let capturedAtLength = 0;

    const { result } = renderHook(() => {
      const lane = useDupesLane(toast, true, { band: null, entityId: null });
      if (capturedSelectAll === null && lane.candidates.length > 0) {
        capturedSelectAll = lane.selectAllVisible;
        capturedAtLength = lane.candidates.length;
      }
      return lane;
    });

    await waitFor(() => expect(result.current.candidates.length).toBe(PAGE));

    // The callback was taken from the commit the reviewer could actually have
    // pressed the shortcut in.
    expect(capturedAtLength).toBe(FIRST_PAINT_ROWS);

    act(() => capturedSelectAll!());

    expect(result.current.selectedIds.size).toBe(FIRST_PAINT_ROWS);
    // ...and specifically the rows that were on screen, not an arbitrary subset.
    expect([...result.current.selectedIds].sort((a, b) => a - b)).toEqual(
      fullPage.slice(0, FIRST_PAINT_ROWS).map((c) => c.id)
    );
  });

  it('extends the mounted rows as a PREFIX, so a shift-click anchor survives', async () => {
    // The shift-click anchor is an INDEX into `visible`, not a candidate id (see
    // the comment on lastClickedIndexRef). An index only keeps its meaning if
    // lifting the cap appends rows rather than reordering them -- which is what
    // lets the anchor be set during the mount window and used after it.
    let capped: api.DedupCandidate[] | null = null;

    const { result } = renderHook(() => {
      const lane = useDupesLane(toast, true, { band: null, entityId: null });
      if (capped === null && lane.candidates.length > 0) capped = lane.candidates;
      return lane;
    });

    await waitFor(() => expect(result.current.candidates.length).toBe(PAGE));

    expect(capped).not.toBeNull();
    expect(capped!.length).toBe(FIRST_PAINT_ROWS);
    expect(capped).toEqual(result.current.candidates.slice(0, FIRST_PAINT_ROWS));
  });

  it('still selects a shift-click range across the whole page once mounted', async () => {
    // Regression guard on the behaviour the prefix property protects: the range
    // still resolves against `visible` after the cap has lifted.
    const { result } = renderHook(() =>
      useDupesLane(toast, true, { band: null, entityId: null })
    );
    await waitFor(() => expect(result.current.candidates.length).toBe(PAGE));

    // Anchor on row index 3, then shift-click index 6.
    act(() => result.current.toggleSelect(fullPage[3].id, 3));
    act(() => result.current.toggleSelect(fullPage[6].id, 6, true));

    expect([...result.current.selectedIds].sort((a, b) => a - b)).toEqual([
      fullPage[3].id,
      fullPage[4].id,
      fullPage[5].id,
      fullPage[6].id,
    ]);
  });

  it('keeps focusedIndex inside the rows that are actually mounted', async () => {
    // focusedIndex is clamped on READ against `visible`, which is now the
    // mounted slice. The clamp has to keep holding, or `j` points the merge
    // shortcuts at a row that is not on screen.
    const seen: { rows: number; focused: number }[] = [];
    const { result } = renderHook(() => {
      const lane = useDupesLane(toast, true, { band: null, entityId: null });
      seen.push({ rows: lane.candidates.length, focused: lane.focusedIndex });
      return lane;
    });

    await waitFor(() => expect(result.current.candidates.length).toBe(PAGE));

    for (const s of seen) {
      if (s.rows === 0) continue;
      expect(s.focused).toBeLessThanOrEqual(s.rows - 1);
      expect(s.focused).toBeGreaterThanOrEqual(0);
    }
  });
});
