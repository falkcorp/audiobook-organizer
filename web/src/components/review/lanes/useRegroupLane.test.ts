// file: web/src/components/review/lanes/useRegroupLane.test.ts
// version: 1.0.0
// guid: 7d3e9b16-2c58-4f07-a4e1-06b8d5c92f3a
// last-edited: 2026-08-20

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
import { useRegroupLane } from './useRegroupLane';

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
