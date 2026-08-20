// file: web/src/components/review/lanes/useRegroupLane.ts
// version: 1.0.0
// guid: 3f8b2c07-9d41-4e56-b8a3-1c7e05d9a264
// last-edited: 2026-08-20

/**
 * The regroup lane's data layer.
 *
 * Shaped like useDupesLane (own fetch, own AbortController, `active` gating) and
 * deliberately NOT like the page it replaces, which read its rows from the
 * globally polled useReviewStore.
 *
 * That store stays. `ReviewBanner`, `Sidebar` and `App.tsx` all read its `count`,
 * and `App.tsx` owns its polling, so deleting it at Phase 7 would silently kill
 * the sidebar badge. What moves here is `items` / `itemsLoading` / `loadItems`,
 * which had exactly one consumer and were lane-private state living in a global
 * store by accident of history.
 *
 * The deciding reason is gating: a lane whose rows live in a globally polled
 * store cannot be switched off when the reviewer moves to another lane, because
 * the store has no notion of who is looking.
 *
 * See docs/port-inventory-regroup.md for the three defects this port fixes
 * rather than carries.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import * as api from '../../../services/api';
import type { ReviewBulkSkip, ReviewItem } from '../../../services/api';
import { useReviewStore } from '../../../stores/useReviewStore';
import { labelForKind } from '../../../lib/reviewKinds';
import { defaultActionFor, parsePayload } from '../../../lib/reviewPayload';

type Toast = (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void;

/**
 * How many holds the lane loads. The source used the same number.
 *
 * It is a real ceiling, not a formality: production currently holds 730 pending
 * items, 714 of them one kind. Everything below that reports a loaded count
 * separately from the true count precisely because those two numbers differ.
 */
export const REGROUP_FETCH_LIMIT = 500;

export interface RegroupBucket {
  kind: string;
  label: string;
  /** The holds actually loaded for this kind — what the reviewer can see. */
  items: ReviewItem[];
  /**
   * Every pending hold of this kind on the server, from the polled count.
   *
   * Separate from `items.length` because they genuinely differ, and the gap is
   * the whole point: "Approve all" is kind-scoped server-side, so it acts on
   * this number, not on the one the reviewer scrolled through.
   */
  totalForKind: number;
  /** True when the server holds more of this kind than the lane loaded. */
  truncated: boolean;
}

export interface RegroupLane {
  loading: boolean;
  error: string | null;

  buckets: RegroupBucket[];
  /** Pending holds across every kind, per the server. */
  total: number;
  /** Holds actually loaded, across every kind. */
  loaded: number;

  /** Resolves what Approve will send for one hold. */
  actionFor: (item: ReviewItem) => string;
  setAction: (id: string, value: string) => void;

  isItemBusy: (id: string) => boolean;
  isKindBusy: (kind: string) => boolean;

  approveItem: (item: ReviewItem) => void;
  rejectItem: (item: ReviewItem) => void;
  bulkAction: (kind: string, action: 'approve' | 'reject') => void;

  /**
   * Skips from the last bulk action, keyed BY KIND.
   *
   * The source held a single `{kind, skipped}` and overwrote it on every bulk
   * call, so acting on a second bucket erased the first bucket's report while
   * those holds were still sitting there undecided. Nothing was destroyed, but
   * the one message telling the reviewer what still needed them vanished.
   */
  skipsByKind: Record<string, ReviewBulkSkip[]>;
  dismissSkips: (kind: string) => void;

  refresh: () => void;
}

export function useRegroupLane(toast: Toast, active = true): RegroupLane {
  const [items, setItems] = useState<ReviewItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyItems, setBusyItems] = useState<Set<string>>(new Set());
  const [busyKinds, setBusyKinds] = useState<Set<string>>(new Set());
  const [chosenActions, setChosenActions] = useState<Record<string, string>>({});
  const [skipsByKind, setSkipsByKind] = useState<Record<string, ReviewBulkSkip[]>>({});
  const [reloadToken, setReloadToken] = useState(0);

  // The badge count is shared, genuinely global, and already polled by App.tsx.
  // The lane READS it for honest per-kind totals rather than counting its own
  // loaded rows and calling that the total.
  const byKind = useReviewStore((s) => s.byKind);
  const loadCount = useReviewStore((s) => s.loadCount);

  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!active) return;
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    setLoading(true);
    setError(null);

    api
      .getReviewItems({ status: 'pending', limit: REGROUP_FETCH_LIMIT }, { signal: ctrl.signal })
      .then((page) => {
        if (ctrl.signal.aborted) return;
        setItems(page.items ?? []);
        setTotal(page.total ?? 0);
        setLoading(false);
      })
      .catch((err: unknown) => {
        // An abort is this hook cancelling its own request, not a failure the
        // reviewer should see.
        if (ctrl.signal.aborted) return;
        setError(err instanceof Error ? err.message : 'Failed to load the review queue');
        setLoading(false);
      });

    return () => ctrl.abort();
  }, [active, reloadToken]);

  // Keep the shared count fresh when the lane opens, so per-kind totals are not
  // stale on first paint while waiting for the next poll tick.
  useEffect(() => {
    if (!active) return;
    void loadCount();
  }, [active, loadCount, reloadToken]);

  const refresh = useCallback(() => setReloadToken((t) => t + 1), []);

  const buckets = useMemo(() => {
    const map = new Map<string, ReviewItem[]>();
    for (const item of items) {
      const list = map.get(item.kind) ?? [];
      list.push(item);
      map.set(item.kind, list);
    }
    return Array.from(map.entries())
      .map(([kind, kindItems]) => {
        // Fall back to the loaded count when the polled map has no entry for
        // this kind. Claiming a total we do not have would be worse than
        // claiming a small one.
        const totalForKind = byKind[kind] ?? kindItems.length;
        return {
          kind,
          label: labelForKind(kind),
          items: kindItems,
          totalForKind,
          truncated: totalForKind > kindItems.length,
        };
      })
      .sort((a, b) => a.label.localeCompare(b.label));
  }, [items, byKind]);

  const actionFor = useCallback(
    (item: ReviewItem): string => {
      const override = chosenActions[item.id];
      if (override !== undefined) return override;
      return defaultActionFor(parsePayload(item.payload));
    },
    [chosenActions]
  );

  const setAction = useCallback((id: string, value: string) => {
    setChosenActions((prev) => ({ ...prev, [id]: value }));
  }, []);

  const isItemBusy = useCallback((id: string) => busyItems.has(id), [busyItems]);
  const isKindBusy = useCallback((kind: string) => busyKinds.has(kind), [busyKinds]);

  const reload = useCallback(async () => {
    await Promise.all([
      api
        .getReviewItems({ status: 'pending', limit: REGROUP_FETCH_LIMIT })
        .then((page) => {
          setItems(page.items ?? []);
          setTotal(page.total ?? 0);
        })
        .catch(() => {
          // A failed refresh after a SUCCESSFUL action must not report the
          // action as failed. The rows are stale, not wrong.
        }),
      loadCount(),
    ]);
  }, [loadCount]);

  const runItemAction = useCallback(
    async (item: ReviewItem, action: 'approve' | 'reject') => {
      setBusyItems((prev) => new Set(prev).add(item.id));
      try {
        if (action === 'approve') {
          // Always send the resolved action explicitly. The backend would accept
          // an empty body and use the recommendation, but sending what the UI
          // actually displayed removes any chance of the two disagreeing.
          await api.approveReviewItem(item.id, actionFor(item));
        } else {
          await api.rejectReviewItem(item.id);
        }
        await reload();
      } catch (err) {
        // The backend's own message is surfaced verbatim, because a refusal
        // carries a reason the reviewer needs to read and a generic "failed to
        // approve" would hide it. Pretending it succeeded would be worse still:
        // the hold would read as decided while nothing had happened.
        //
        // This used to be justified by `duplicate-of` answering 501. That is no
        // longer true -- duplicate-of has had an apply path since 2026-08-19 --
        // but the behaviour is right on its own terms, so it is the REASON that
        // was rewritten here, not the code.
        toast(
          `Failed to ${action} item: ${err instanceof Error ? err.message : 'unknown error'}`,
          'error'
        );
      } finally {
        setBusyItems((prev) => {
          const next = new Set(prev);
          next.delete(item.id);
          return next;
        });
      }
    },
    [actionFor, reload, toast]
  );

  const approveItem = useCallback(
    (item: ReviewItem) => void runItemAction(item, 'approve'),
    [runItemAction]
  );
  const rejectItem = useCallback(
    (item: ReviewItem) => void runItemAction(item, 'reject'),
    [runItemAction]
  );

  const bulkAction = useCallback(
    (kind: string, action: 'approve' | 'reject') => {
      void (async () => {
        setBusyKinds((prev) => new Set(prev).add(kind));
        try {
          const result = await api.bulkReviewAction({ action, kind });
          const skipped = result.skipped ?? [];
          // Report the skips in the same breath as the successes. Bulk approve
          // runs each hold's OWN recommendation and refuses the undecidable
          // ones; a message quoting only `processed` would let a reviewer
          // believe a bucket was cleared when a third of it is still sitting
          // there.
          toast(
            `${action === 'approve' ? 'Approved' : 'Rejected'} ${result.processed} item${
              result.processed === 1 ? '' : 's'
            }${skipped.length ? ` · ${skipped.length} skipped, listed below` : ''}`,
            skipped.length ? 'warning' : 'success'
          );
          setSkipsByKind((prev) => {
            const next = { ...prev };
            if (skipped.length) next[kind] = skipped;
            else delete next[kind];
            return next;
          });
          await reload();
        } catch (err) {
          toast(
            `Bulk ${action} failed: ${err instanceof Error ? err.message : 'unknown error'}`,
            'error'
          );
        } finally {
          setBusyKinds((prev) => {
            const next = new Set(prev);
            next.delete(kind);
            return next;
          });
        }
      })();
    },
    [reload, toast]
  );

  const dismissSkips = useCallback((kind: string) => {
    setSkipsByKind((prev) => {
      const next = { ...prev };
      delete next[kind];
      return next;
    });
  }, []);

  return {
    loading,
    error,
    buckets,
    total,
    loaded: items.length,
    actionFor,
    setAction,
    isItemBusy,
    isKindBusy,
    approveItem,
    rejectItem,
    bulkAction,
    skipsByKind,
    dismissSkips,
    refresh,
  };
}
