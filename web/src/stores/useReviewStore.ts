// file: web/src/stores/useReviewStore.ts
// version: 1.0.0
// guid: 1e9d4c72-8a36-4f50-b1c7-3d2e6a9b7c81
// last-edited: 2026-07-13

import { create } from 'zustand';
import * as api from '../services/api';
import { type ReviewItem, type ReviewItemsFilter } from '../services/api';

// Default poll cadence for the review count. The review queue is low-volume
// (intentional holds only, never raw backlogs — decision #1), so a slow poll is
// plenty; SSE is an optional fast-follow.
const DEFAULT_POLL_INTERVAL_MS = 30_000;

interface ReviewState {
  /** Number of PENDING items — drives the banner + sidebar badge. */
  count: number;
  /** Pending count broken down by Kind (regroup.multidisc → 12, ...). */
  byKind: Record<string, number>;
  /** Items loaded for the /review page. */
  items: ReviewItem[];
  /** True while loadItems is in flight (for the page's loading state). */
  itemsLoading: boolean;
  /** setInterval handle for the count poller — kept here so it can be cleared
   *  on stopPolling, mirroring useOperationsStore's _sseSource. */
  _pollTimer: ReturnType<typeof setInterval> | null;

  /** loadCount fetches the pending count + byKind breakdown (REST). */
  loadCount: () => Promise<void>;
  /** loadItems fetches items for the given filter (default: pending). */
  loadItems: (filter?: ReviewItemsFilter) => Promise<void>;
  /** startPolling begins the count poller. Calling it again while a poller is
   *  already running is a no-op (guards against double-start across auth
   *  transitions), mirroring useOperationsStore.openSSE's guard. */
  startPolling: (intervalMs?: number) => void;
  /** stopPolling tears down the count poller. */
  stopPolling: () => void;
}

export const useReviewStore = create<ReviewState>()((set, get) => ({
  count: 0,
  byKind: {},
  items: [],
  itemsLoading: false,
  _pollTimer: null,

  loadCount: async () => {
    try {
      const { count, byKind } = await api.getReviewCount();
      set({ count, byKind });
    } catch (err) {
      // Non-critical — the badge/banner just stays at its last value.
      console.error('Failed to load review count', err);
    }
  },

  loadItems: async (filter: ReviewItemsFilter = {}) => {
    set({ itemsLoading: true });
    try {
      const page = await api.getReviewItems({ status: 'pending', limit: 500, ...filter });
      set({ items: page.items });
    } catch (err) {
      console.error('Failed to load review items', err);
      set({ items: [] });
    } finally {
      set({ itemsLoading: false });
    }
  },

  startPolling: (intervalMs = DEFAULT_POLL_INTERVAL_MS) => {
    // Guard: don't spin up a second interval if one is already running.
    if (get()._pollTimer !== null) return;
    // Fire once immediately so the badge reflects reality without waiting a
    // full interval.
    void get().loadCount();
    const timer = setInterval(() => void get().loadCount(), intervalMs);
    set({ _pollTimer: timer });
  },

  stopPolling: () => {
    const timer = get()._pollTimer;
    if (timer !== null) {
      clearInterval(timer);
      set({ _pollTimer: null });
    }
  },
}));
