// file: web/src/stores/useReviewStore.ts
// version: 1.1.0
// guid: 1e9d4c72-8a36-4f50-b1c7-3d2e6a9b7c81
// last-edited: 2026-09-01

import { create } from 'zustand';
import * as api from '../services/api';
import { type ReviewItem, type ReviewItemsFilter } from '../services/api';

/**
 * True when two count breakdowns hold exactly the same kinds and the same
 * numbers. Used to decide whether a poll tick has anything to publish.
 */
function sameCounts(a: Record<string, number>, b: Record<string, number>): boolean {
  const aKeys = Object.keys(a);
  if (aKeys.length !== Object.keys(b).length) return false;
  for (const k of aKeys) {
    // `Object.prototype.hasOwnProperty` matters: a kind present with value
    // `undefined` is not the same as a kind that is absent, and `b[k] === a[k]`
    // alone would call those equal.
    if (!Object.prototype.hasOwnProperty.call(b, k)) return false;
    if (a[k] !== b[k]) return false;
  }
  return true;
}

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
      // Publish a NEW `byKind` object only when the numbers actually moved.
      //
      // This poller ticks every 30s for the life of the session. A freshly
      // parsed object is never `Object.is`-equal to the last one, so installing
      // it unconditionally woke every `(s) => s.byKind` subscriber twice a
      // minute even when nothing had changed. On /review that subscriber is
      // useRegroupLane, whose `buckets` useMemo is keyed on `byKind` -- so the
      // new identity rebuilt the buckets, produced a new lane object, and
      // re-rendered ReviewWorkspace and every panel under it, in ALL THREE
      // lanes, regardless of which one was visible.
      //
      // `count` is a primitive, so zustand's own `Object.is` check already
      // absorbed it; `byKind` was the only identity churn on this path.
      const prev = get().byKind;
      set(sameCounts(prev, byKind) ? { count } : { count, byKind });
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
