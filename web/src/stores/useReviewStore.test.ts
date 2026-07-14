// file: web/src/stores/useReviewStore.test.ts
// version: 1.0.0
// guid: 8d1f6b93-4a27-4c50-9e83-2b7c5d0a6f14
// last-edited: 2026-07-13

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
