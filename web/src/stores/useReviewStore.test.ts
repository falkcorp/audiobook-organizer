// file: web/src/stores/useReviewStore.test.ts
// version: 1.1.0
// guid: 8d1f6b93-4a27-4c50-9e83-2b7c5d0a6f14
// last-edited: 2026-09-01

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { useReviewStore } from './useReviewStore';
import * as api from '../services/api';

vi.mock('../services/api');

describe('useReviewStore', () => {
  beforeEach(() => {
    useReviewStore.setState({
      count: 0,
      byKind: {},
      items: [],
      itemsLoading: false,
      _pollTimer: null,
    });
    vi.restoreAllMocks();
  });

  it('loadCount refreshes count + byKind from the count endpoint', async () => {
    vi.mocked(api.getReviewCount).mockResolvedValue({
      count: 7,
      byKind: { 'regroup.multidisc': 5, 'regroup.ambiguous': 2 },
    });

    await useReviewStore.getState().loadCount();

    expect(api.getReviewCount).toHaveBeenCalledTimes(1);
    expect(useReviewStore.getState().count).toBe(7);
    expect(useReviewStore.getState().byKind).toEqual({
      'regroup.multidisc': 5,
      'regroup.ambiguous': 2,
    });
  });

  // The poller ticks every 30s for the life of the session. `byKind` is read by
  // useRegroupLane through a `(s) => s.byKind` selector and feeds a useMemo dep
  // array, so a new object identity on an unchanged tick re-renders the whole
  // review route. These four assert on IDENTITY, which is the thing that
  // actually drives the re-render -- `toEqual` cannot see the bug at all.
  describe('loadCount byKind identity', () => {
    it('keeps the SAME byKind object when the counts have not moved', async () => {
      vi.mocked(api.getReviewCount).mockResolvedValue({
        count: 7,
        byKind: { 'regroup.multidisc': 5, 'regroup.ambiguous': 2 },
      });

      await useReviewStore.getState().loadCount();
      const first = useReviewStore.getState().byKind;

      // A second tick returns a FRESHLY PARSED object with identical numbers,
      // exactly as a real JSON response would.
      vi.mocked(api.getReviewCount).mockResolvedValue({
        count: 7,
        byKind: { 'regroup.multidisc': 5, 'regroup.ambiguous': 2 },
      });
      await useReviewStore.getState().loadCount();

      expect(useReviewStore.getState().byKind).toBe(first);
    });

    it('installs a NEW byKind object when a count changes', async () => {
      vi.mocked(api.getReviewCount).mockResolvedValue({
        count: 7,
        byKind: { 'regroup.multidisc': 5 },
      });
      await useReviewStore.getState().loadCount();
      const first = useReviewStore.getState().byKind;

      vi.mocked(api.getReviewCount).mockResolvedValue({
        count: 8,
        byKind: { 'regroup.multidisc': 6 },
      });
      await useReviewStore.getState().loadCount();

      expect(useReviewStore.getState().byKind).not.toBe(first);
      expect(useReviewStore.getState().byKind).toEqual({ 'regroup.multidisc': 6 });
      expect(useReviewStore.getState().count).toBe(8);
    });

    it('installs a NEW byKind object when a kind APPEARS', async () => {
      vi.mocked(api.getReviewCount).mockResolvedValue({
        count: 5,
        byKind: { 'regroup.multidisc': 5 },
      });
      await useReviewStore.getState().loadCount();
      const first = useReviewStore.getState().byKind;

      // Same total, but a new kind: a length check alone would catch this, a
      // per-key value check alone would not.
      vi.mocked(api.getReviewCount).mockResolvedValue({
        count: 5,
        byKind: { 'regroup.multidisc': 3, 'regroup.ambiguous': 2 },
      });
      await useReviewStore.getState().loadCount();

      expect(useReviewStore.getState().byKind).not.toBe(first);
      expect(useReviewStore.getState().byKind).toEqual({
        'regroup.multidisc': 3,
        'regroup.ambiguous': 2,
      });
    });

    it('installs a NEW byKind object when a kind DISAPPEARS', async () => {
      vi.mocked(api.getReviewCount).mockResolvedValue({
        count: 5,
        byKind: { 'regroup.multidisc': 5, 'regroup.ambiguous': 0 },
      });
      await useReviewStore.getState().loadCount();
      const first = useReviewStore.getState().byKind;

      vi.mocked(api.getReviewCount).mockResolvedValue({
        count: 5,
        byKind: { 'regroup.multidisc': 5 },
      });
      await useReviewStore.getState().loadCount();

      expect(useReviewStore.getState().byKind).not.toBe(first);
      expect(useReviewStore.getState().byKind).toEqual({ 'regroup.multidisc': 5 });
    });

    it('still updates count when only the count moved and byKind is held', async () => {
      // byKind identical, count different. The count MUST still land -- an
      // early `return` that skipped the whole `set` would freeze the badge.
      vi.mocked(api.getReviewCount).mockResolvedValue({ count: 7, byKind: { a: 1 } });
      await useReviewStore.getState().loadCount();
      const first = useReviewStore.getState().byKind;

      vi.mocked(api.getReviewCount).mockResolvedValue({ count: 9, byKind: { a: 1 } });
      await useReviewStore.getState().loadCount();

      expect(useReviewStore.getState().byKind).toBe(first);
      expect(useReviewStore.getState().count).toBe(9);
    });
  });

  it('loadCount keeps the last value on error (non-critical)', async () => {
    useReviewStore.setState({ count: 3 });
    vi.mocked(api.getReviewCount).mockRejectedValue(new Error('boom'));

    await useReviewStore.getState().loadCount();

    // Failure must not zero the badge — it stays at the last known value.
    expect(useReviewStore.getState().count).toBe(3);
  });

  it('loadItems populates items and toggles the loading flag', async () => {
    vi.mocked(api.getReviewItems).mockResolvedValue({
      items: [
        {
          id: 'r1',
          kind: 'regroup.multidisc',
          dedup_key: 'k1',
          folder_ref: '/a/b',
          status: 'pending',
          summary: 'Disc 1/2',
          payload: '{}',
          created_at: '2026-07-13T00:00:00Z',
          updated_at: '2026-07-13T00:00:00Z',
        },
      ],
      count: 1,
      limit: 500,
      offset: 0,
      total: 1,
    });

    await useReviewStore.getState().loadItems({ status: 'pending' });

    expect(api.getReviewItems).toHaveBeenCalledWith(
      expect.objectContaining({ status: 'pending' })
    );
    expect(useReviewStore.getState().items).toHaveLength(1);
    expect(useReviewStore.getState().itemsLoading).toBe(false);
  });
});
