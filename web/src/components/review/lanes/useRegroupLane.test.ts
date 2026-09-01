// file: web/src/components/review/lanes/useRegroupLane.test.ts
// version: 1.1.0
// guid: 7d3e9b16-2c58-4f07-a4e1-06b8d5c92f3a
// last-edited: 2026-09-01

/**
 * Tests for the regroup lane's data layer.
 *
 * Weighted toward the three defects docs/port-inventory-regroup.md found in the
 * page this replaces, because a mechanical port would have carried all three.
 */

import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import { useReviewStore } from '../../../stores/useReviewStore';
import { REGROUP_SEARCH_DEBOUNCE_MS, useRegroupLane } from './useRegroupLane';

vi.mock('../../../services/api');

const toast = vi.fn();

function makeItem(id: string, kind: string, payload: object = {}): api.ReviewItem {
  return {
    id,
    kind,
    dedup_key: `dk-${id}`,
    folder_ref: `/audiobooks/${id}`,
    status: 'pending',
    summary: `Hold ${id}`,
    payload: JSON.stringify(payload),
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  } as api.ReviewItem;
}

function mockItems(items: api.ReviewItem[], total = items.length) {
  vi.mocked(api.getReviewItems).mockResolvedValue({
    items,
    count: items.length,
    limit: 500,
    offset: 0,
    total,
  });
}

/**
 * Sets the polled per-kind counts the badge already maintains.
 *
 * Seeds BOTH the store and the endpoint behind it: the lane refreshes the count
 * when it opens, so a fixture that only wrote the store would be overwritten by
 * the mock's default a tick later.
 */
function setByKind(byKind: Record<string, number>) {
  const count = Object.values(byKind).reduce((a, b) => a + b, 0);
  useReviewStore.setState({ byKind, count });
  vi.mocked(api.getReviewCount).mockResolvedValue({ count, byKind });
}

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockReset();
  setByKind({});
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, byKind: {} });
  mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('m1', 'regroup.multidisc')]);
});

async function renderLane(active = true) {
  const view = renderHook(() => useRegroupLane(toast, active));
  await waitFor(() => expect(view.result.current.loading).toBe(false));
  return view;
}

// ---------------------------------------------------------------------------
// The widening bug: what the bucket shows vs. what "Approve all" acts on
// ---------------------------------------------------------------------------

describe('bucket counts tell the truth about bulk scope', () => {
  it('reports the true per-kind total, not the number loaded', async () => {
    // The live shape when this was written: 714 pending of one kind, 500 loaded,
    // 484 of them this kind. The page showed 484 and the button acted on 714.
    setByKind({ 'regroup.ambiguous': 714, 'regroup.multidisc': 16 });
    const loaded = Array.from({ length: 484 }, (_, i) => makeItem(`a${i}`, 'regroup.ambiguous'));
    mockItems([...loaded, makeItem('m1', 'regroup.multidisc')], 730);

    const { result } = await renderLane();
    const bucket = result.current.buckets.find((b) => b.kind === 'regroup.ambiguous');

    expect(bucket?.items).toHaveLength(484);
    expect(bucket?.totalForKind).toBe(714);
    expect(bucket?.truncated).toBe(true);
  });

  it('is not truncated when the server holds no more than was loaded', async () => {
    setByKind({ 'regroup.ambiguous': 1, 'regroup.multidisc': 1 });
    const { result } = await renderLane();
    expect(result.current.buckets.every((b) => b.truncated)).toBe(false);
  });

  it('falls back to the loaded count when the polled map has no entry', async () => {
    // Claiming a total we do not have would be worse than claiming a small one.
    setByKind({});
    const { result } = await renderLane();
    const bucket = result.current.buckets.find((b) => b.kind === 'regroup.ambiguous');
    expect(bucket?.totalForKind).toBe(1);
    expect(bucket?.truncated).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// The skip report that used to erase itself
// ---------------------------------------------------------------------------

describe('bulk skips are kept per kind', () => {
  it('acting on a second kind does not erase the first kind report', async () => {
    // The source held ONE {kind, skipped}. Bulk-approve A, start reading its
    // twelve skips, bulk-approve B -- and A's list vanished while A's holds were
    // still sitting there undecided.
    const { result } = await renderLane();

    vi.mocked(api.bulkReviewAction).mockResolvedValueOnce({
      action: 'approve',
      processed: 3,
      skipped: [{ id: 'a1', action: '', reason: 'insufficient-evidence' }],
    } as api.ReviewBulkResult);
    await act(async () => {
      result.current.bulkAction('regroup.ambiguous', 'approve');
    });
    await waitFor(() =>
      expect(result.current.skipsByKind['regroup.ambiguous']).toHaveLength(1)
    );

    vi.mocked(api.bulkReviewAction).mockResolvedValueOnce({
      action: 'approve',
      processed: 1,
      skipped: [{ id: 'm1', action: '', reason: 'insufficient-evidence' }],
    } as api.ReviewBulkResult);
    await act(async () => {
      result.current.bulkAction('regroup.multidisc', 'approve');
    });
    await waitFor(() => expect(result.current.skipsByKind['regroup.multidisc']).toHaveLength(1));

    // The load-bearing assertion.
    expect(result.current.skipsByKind['regroup.ambiguous']).toHaveLength(1);
  });

  it('dismissing one kind leaves the others alone', async () => {
    const { result } = await renderLane();
    vi.mocked(api.bulkReviewAction).mockResolvedValue({
      action: 'approve',
      processed: 1,
      skipped: [{ id: 'x', action: '', reason: 'insufficient-evidence' }],
    } as api.ReviewBulkResult);

    await act(async () => result.current.bulkAction('regroup.ambiguous', 'approve'));
    await act(async () => result.current.bulkAction('regroup.multidisc', 'approve'));
    act(() => result.current.dismissSkips('regroup.ambiguous'));

    expect(result.current.skipsByKind['regroup.ambiguous']).toBeUndefined();
    expect(result.current.skipsByKind['regroup.multidisc']).toHaveLength(1);
  });

  it('a clean bulk run clears that kind rather than leaving a stale report', async () => {
    const { result } = await renderLane();
    vi.mocked(api.bulkReviewAction).mockResolvedValueOnce({
      action: 'approve',
      processed: 1,
      skipped: [{ id: 'a1', action: '', reason: 'insufficient-evidence' }],
    } as api.ReviewBulkResult);
    await act(async () => result.current.bulkAction('regroup.ambiguous', 'approve'));
    await waitFor(() => expect(result.current.skipsByKind['regroup.ambiguous']).toBeDefined());

    vi.mocked(api.bulkReviewAction).mockResolvedValueOnce({
      action: 'approve',
      processed: 1,
      skipped: [],
    } as unknown as api.ReviewBulkResult);
    await act(async () => result.current.bulkAction('regroup.ambiguous', 'approve'));

    await waitFor(() =>
      expect(result.current.skipsByKind['regroup.ambiguous']).toBeUndefined()
    );
  });
});

// ---------------------------------------------------------------------------
// Action resolution and per-item behaviour
// ---------------------------------------------------------------------------

describe('actionFor', () => {
  it("prefers the reviewer's explicit pick over the recommendation", async () => {
    mockItems([makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'combine' })]);
    const { result } = await renderLane();
    const item = result.current.buckets[0].items[0];

    expect(result.current.actionFor(item)).toBe('combine');
    act(() => result.current.setAction('a1', 'separate'));
    await waitFor(() => expect(result.current.actionFor(item)).toBe('separate'));
  });

  it('resolves to empty for an undecidable hold, which is what disables Approve', async () => {
    // insufficient-evidence is a statement BY the machine, not a decision a
    // human can take. Pre-filling a guess here would put `combine` on precisely
    // the holds with the least evidence.
    mockItems([makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'insufficient-evidence' })]);
    const { result } = await renderLane();
    expect(result.current.actionFor(result.current.buckets[0].items[0])).toBe('');
  });

  it('sends the resolved action explicitly on approve', async () => {
    mockItems([makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'combine' })]);
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    const { result } = await renderLane();

    await act(async () => {
      result.current.approveItem(result.current.buckets[0].items[0]);
    });

    // Not an empty body relying on the server to re-derive it: what ran and what
    // was displayed cannot then disagree.
    expect(api.approveReviewItem).toHaveBeenCalledWith('a1', 'combine');
  });

  it("surfaces the backend's own message when an action is refused", async () => {
    vi.mocked(api.approveReviewItem).mockRejectedValue(new Error('nope, owned by maintenance'));
    const { result } = await renderLane();

    await act(async () => {
      result.current.approveItem(result.current.buckets[0].items[0]);
    });

    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining('nope, owned by maintenance'),
      'error'
    );
  });
});

// ---------------------------------------------------------------------------
// Lane gating -- the reason this does not read from the global store
// ---------------------------------------------------------------------------

describe('the lane only fetches while it is showing', () => {
  it('does not fetch when inactive', async () => {
    renderHook(() => useRegroupLane(toast, false));
    await new Promise((r) => setTimeout(r, 20));
    expect(api.getReviewItems).not.toHaveBeenCalled();
  });

  it('aborts the in-flight request when the lane is switched away', async () => {
    // Not just "ignores the response": getReviewItems took no signal at all
    // until this lane needed one, so the request ran to completion against a
    // lane nobody was looking at. The assertion is on the SIGNAL, because
    // discarding a result you still paid for is not cancellation.
    let captured: AbortSignal | undefined;
    vi.mocked(api.getReviewItems).mockImplementation((_filter, opts) => {
      captured = opts?.signal;
      return new Promise(() => {
        // never resolves; the abort is the whole point
      });
    });

    const { rerender } = renderHook(({ on }) => useRegroupLane(toast, on), {
      initialProps: { on: true },
    });
    await waitFor(() => expect(captured).toBeDefined());
    expect(captured?.aborted).toBe(false);

    rerender({ on: false });
    expect(captured?.aborted).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// The kind filter: the only one of the three that leaves the browser
// ---------------------------------------------------------------------------

/** makeItem with an explicit created_at, for the ordering tests. */
function makeItemAt(id: string, kind: string, createdAt: string): api.ReviewItem {
  return { ...makeItem(id, kind), created_at: createdAt };
}

/** The filter object of the Nth getReviewItems call (0-based). */
function filterOfCall(n: number): api.ReviewItemsFilter {
  return vi.mocked(api.getReviewItems).mock.calls[n][0] as api.ReviewItemsFilter;
}

function lastFilter(): api.ReviewItemsFilter {
  const calls = vi.mocked(api.getReviewItems).mock.calls;
  return calls[calls.length - 1][0] as api.ReviewItemsFilter;
}

describe('the kind filter is pushed to the server', () => {
  it('sends no kind at all until one is chosen', async () => {
    await renderLane();
    // Not `kind: ''`. getReviewItems only sets the param for a truthy kind, but
    // asserting the ABSENCE here is what stops a future refactor from sending an
    // empty kind and quietly filtering for the empty-string kind.
    expect(filterOfCall(0).kind).toBeUndefined();
    expect(filterOfCall(0)).toMatchObject({ status: 'pending', limit: 500 });
  });

  it('refetches with the chosen kind rather than filtering the loaded page', async () => {
    // 🔴 The whole point of item 1. Loading 500 mixed rows and hiding the
    // unwanted ones spends the page budget on rows nobody asked for; on the live
    // queue that is ~484 of the worked kind instead of 500.
    const { result } = await renderLane();
    expect(api.getReviewItems).toHaveBeenCalledTimes(1);

    await act(async () => {
      result.current.setFilters({ kind: 'regroup.multidisc' });
    });
    await waitFor(() => expect(api.getReviewItems).toHaveBeenCalledTimes(2));

    expect(filterOfCall(1)).toMatchObject({
      status: 'pending',
      limit: 500,
      kind: 'regroup.multidisc',
    });
  });

  it('keeps the kind on the reload that follows an item action', async () => {
    // 🔴 THE SECOND CALL SITE. reload() runs after every approve, reject and
    // bulk action. An unfiltered reload here would silently repopulate the lane
    // with every kind the moment a reviewer decided one hold -- and a test that
    // only checked the mount fetch would pass while it happened.
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    mockItems([makeItem('m1', 'regroup.multidisc', { recommendedAction: 'combine' })]);
    const { result } = await renderLane();

    await act(async () => {
      result.current.setFilters({ kind: 'regroup.multidisc' });
    });
    await waitFor(() => expect(lastFilter().kind).toBe('regroup.multidisc'));

    await act(async () => {
      result.current.approveItem(result.current.buckets[0].items[0]);
    });

    expect(api.approveReviewItem).toHaveBeenCalled();
    expect(lastFilter().kind).toBe('regroup.multidisc');
  });

  it('drops the previous kind rows while the new request is in flight', async () => {
    // A lane that is fetching must not look like a lane that has answered. The
    // spine only spins when it has NOTHING, so leaving the old kind's rows up
    // would render the previous answer under the new heading.
    const { result } = await renderLane();
    expect(result.current.buckets.length).toBeGreaterThan(0);

    vi.mocked(api.getReviewItems).mockImplementation(() => new Promise(() => {}));
    await act(async () => {
      result.current.setFilters({ kind: 'regroup.multidisc' });
    });

    await waitFor(() => expect(result.current.buckets).toHaveLength(0));
    expect(result.current.loading).toBe(true);
    expect(result.current.loaded).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// Four counts, four meanings
// ---------------------------------------------------------------------------

describe('the counts stay distinct under a filter', () => {
  it('reads the all-kinds total from the polled count once a kind is selected', async () => {
    // 🔴 The server applies `kind` BEFORE taking the length, so the fetched
    // `total` under a filter is that KIND's count. Rendering it as "N pending"
    // beside the selector would understate the queue by every other kind.
    setByKind({ 'regroup.ambiguous': 714, 'regroup.multidisc': 16 });
    mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('m1', 'regroup.multidisc')], 730);
    const { result } = await renderLane();
    // Unfiltered, the fetched total IS the queue and is preferred: it is the
    // fresher of the two, having just been read.
    expect(result.current.queueTotal).toBe(730);

    mockItems([makeItem('m1', 'regroup.multidisc')], 16);
    await act(async () => {
      result.current.setFilters({ kind: 'regroup.multidisc' });
    });
    await waitFor(() => expect(result.current.total).toBe(16));

    // The fetch total is the KIND's; the queue total is still the queue's.
    expect(result.current.total).toBe(16);
    expect(result.current.queueTotal).toBe(730);
  });

  it('a search narrows `visible` without touching `loaded` or `truncated`', async () => {
    // 🔴 THE INTERACTION THAT WOULD SHIP BROKEN. Truncation means the lane
    // failed to load rows that exist; a search hiding rows is the reviewer
    // asking for that. Derive one from the other and every keystroke raises a
    // "your view is partial" warning, which makes the real one unreadable.
    setByKind({ 'regroup.ambiguous': 2 });
    mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('a2', 'regroup.ambiguous')], 2);
    const { result } = await renderLane();
    expect(result.current.buckets[0].truncated).toBe(false);

    await act(async () => {
      result.current.setFilters({ search: 'a1' });
    });
    await waitFor(() => expect(result.current.visible).toBe(1));

    const bucket = result.current.buckets[0];
    expect(result.current.loaded).toBe(2);
    expect(bucket.loadedForKind).toBe(2);
    expect(bucket.items).toHaveLength(1);
    expect(bucket.hiddenBySearch).toBe(1);
    // The load-bearing assertion.
    expect(bucket.truncated).toBe(false);
  });

  it('offers every kind the SERVER holds, not merely the loaded ones', async () => {
    // A kind pushed off the end of a truncated page would otherwise be missing
    // from the one control that could bring it back.
    setByKind({ 'regroup.ambiguous': 714, 'regroup.anthology': 3 });
    mockItems([makeItem('a1', 'regroup.ambiguous')], 717);
    const { result } = await renderLane();

    expect(result.current.kindOptions.map((k) => k.kind)).toContain('regroup.anthology');
    expect(result.current.kindOptions.find((k) => k.kind === 'regroup.anthology')?.count).toBe(3);
  });
});

// ---------------------------------------------------------------------------
// Search: what it matches, and that it waits
// ---------------------------------------------------------------------------

describe('search over the loaded holds', () => {
  it('matches the summary, the folder path, the proposed title and a member file', async () => {
    mockItems([
      makeItem('a1', 'regroup.ambiguous', { survivorTitle: 'The Silmarillion' }),
      makeItem('a2', 'regroup.ambiguous', { files: ['/mnt/media/tolkien/silm-01.m4b'] }),
      makeItem('a3', 'regroup.ambiguous', { folder: '/mnt/media/asimov' }),
    ]);
    const { result } = await renderLane();

    // 🔴 WAIT ON THE IDS, NOT ON THE COUNT. Every query below happens to leave
    // exactly one row visible, so `waitFor(visible === 1)` is already satisfied
    // by the PREVIOUS query's result and returns before the debounce has moved
    // anything. That is not a hypothetical: it passed this test against the
    // wrong row until the assertion named the row.
    const expectOnly = async (q: string, id: string) => {
      await act(async () => result.current.setFilters({ search: q }));
      await waitFor(() =>
        expect(result.current.buckets.flatMap((b) => b.items.map((i) => i.id))).toEqual([id])
      );
    };

    await expectOnly('silmarillion', 'a1');
    await expectOnly('silm-01', 'a2');
    await expectOnly('asimov', 'a3');

    // The item's own summary, which is what a hold with no payload shows.
    await expectOnly('hold a3', 'a3');

    // And the item's own folder_ref, which is the fallback the row renders when
    // the payload carries no folder of its own.
    await expectOnly('/audiobooks/a2', 'a2');
  });

  it('waits for the debounce before narrowing the buckets', async () => {
    // The search never leaves the browser, so removing the debounce changes no
    // REQUEST -- which is exactly why a request-counting test cannot see it.
    // Fake timers and the visible count can.
    mockItems([
      makeItem('a1', 'regroup.ambiguous'),
      makeItem('a2', 'regroup.ambiguous'),
      makeItem('a3', 'regroup.ambiguous'),
    ]);
    const { result } = await renderLane();
    expect(result.current.visible).toBe(3);

    // Fake timers only now: faking before the initial load makes that load
    // resolve outside act().
    vi.useFakeTimers();
    try {
      act(() => {
        result.current.setFilters({ search: 'a1' });
      });

      // The field has the text immediately -- typing must never lag.
      expect(result.current.filters.search).toBe('a1');
      // The buckets have NOT moved yet. This is the assertion the missing
      // debounce fails.
      expect(result.current.visible).toBe(3);

      await act(async () => {
        vi.advanceTimersByTime(REGROUP_SEARCH_DEBOUNCE_MS);
      });

      expect(result.current.visible).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('clearFilters drops the pending debounced query in the same tick', async () => {
    // Leaving it to the timer would mean "Clear filters" visibly did nothing for
    // a quarter of a second.
    mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('a2', 'regroup.ambiguous')]);
    const { result } = await renderLane();

    await act(async () => result.current.setFilters({ search: 'a1' }));
    await waitFor(() => expect(result.current.visible).toBe(1));

    act(() => result.current.clearFilters());
    expect(result.current.filters.search).toBe('');
    expect(result.current.visible).toBe(2);
    expect(result.current.filtersActive).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Sort: a TOTAL comparator, because equal rows must not reshuffle
// ---------------------------------------------------------------------------

describe('sort ordering', () => {
  // Insertion order is deliberately NOT the answer for any of these. A fixture
  // whose correct order happens to equal its input order cannot observe a
  // comparator that returns 0 -- Array.prototype.sort is stable, so it would
  // hand the input straight back and the assertion would pass.
  const scrambled = [
    makeItemAt('b', 'regroup.ambiguous', '2026-03-01T00:00:00Z'),
    makeItemAt('a', 'regroup.ambiguous', '2026-01-01T00:00:00Z'),
    makeItemAt('c', 'regroup.ambiguous', '2026-02-01T00:00:00Z'),
  ];

  const idsOf = (result: { current: { buckets: { items: api.ReviewItem[] }[] } }) =>
    result.current.buckets.flatMap((b) => b.items.map((i) => i.id));

  it('newest first orders by created_at descending', async () => {
    mockItems(scrambled);
    const { result } = await renderLane();
    await act(async () => result.current.setFilters({ sortBy: 'newest' }));
    await waitFor(() => expect(idsOf(result)).toEqual(['b', 'c', 'a']));
  });

  it('oldest first orders by created_at ascending', async () => {
    mockItems(scrambled);
    const { result } = await renderLane();
    await act(async () => result.current.setFilters({ sortBy: 'oldest' }));
    await waitFor(() => expect(idsOf(result)).toEqual(['a', 'c', 'b']));
  });

  it('breaks a created_at tie on the id rather than leaving it to the fetch order', async () => {
    // 🔴 Ties are the NORMAL case here, not the exotic one: the queue is written
    // in bulk by a scan, so holds sharing a created_at to the second are routine.
    // A comparator that returned 0 for them would leave their order at the mercy
    // of whatever the server handed back, and the list would reshuffle between
    // renders. Insertion order below is [zz, aa]; the id tie-break says [aa, zz].
    mockItems([
      makeItemAt('zz', 'regroup.ambiguous', '2026-01-01T00:00:00Z'),
      makeItemAt('aa', 'regroup.ambiguous', '2026-01-01T00:00:00Z'),
    ]);
    const { result } = await renderLane();

    await act(async () => result.current.setFilters({ sortBy: 'oldest' }));
    await waitFor(() => expect(idsOf(result)).toEqual(['aa', 'zz']));

    // And descending on the other axis, so a tie-break hard-coded to one
    // direction is caught too.
    await act(async () => result.current.setFilters({ sortBy: 'newest' }));
    await waitFor(() => expect(idsOf(result)).toEqual(['zz', 'aa']));
  });

  it('kind sort keeps buckets alphabetical by label, which is the pre-filter behaviour', async () => {
    // 'Ambiguous folders' < 'Multi-disc groups'. The default must reproduce what
    // the lane did before there was a sort control at all.
    mockItems([
      makeItemAt('m1', 'regroup.multidisc', '2026-05-01T00:00:00Z'),
      makeItemAt('a1', 'regroup.ambiguous', '2026-01-01T00:00:00Z'),
    ]);
    const { result } = await renderLane();

    expect(result.current.filters.sortBy).toBe('kind');
    expect(result.current.buckets.map((b) => b.kind)).toEqual([
      'regroup.ambiguous',
      'regroup.multidisc',
    ]);

    // Newest-first reorders the BUCKETS too, by their leading hold -- the sort
    // is over the whole visible set, merely rendered grouped.
    await act(async () => result.current.setFilters({ sortBy: 'newest' }));
    await waitFor(() =>
      expect(result.current.buckets.map((b) => b.kind)).toEqual([
        'regroup.multidisc',
        'regroup.ambiguous',
      ])
    );
  });
});
