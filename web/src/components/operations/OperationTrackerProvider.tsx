// file: web/src/components/operations/OperationTrackerProvider.tsx
// version: 1.0.0
// guid: 4a9c2e7d-1b53-4f80-9d6e-3c7a0b5f8e12
// last-edited: 2026-10-26

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type JSX,
  type ReactNode,
} from 'react';
import { useNavigate } from 'react-router-dom';
import * as api from '../../services/api';
import { useToast } from '../toast/ToastProvider';
import { isAbortError } from '../../utils/apiFetch';
import {
  OPERATION_POLL_TIMEOUT_MS,
  describeFollowTrouble,
  followOperation,
  isOperationLostError,
} from '../../utils/followOperation';
import { describeMergeOutcome, type BookMergeOpResult } from '../../utils/mergeOutcome';
import { describeCombineOutcome, type BookCombineOpResult } from '../../utils/combineOutcome';
import {
  describeUndoFailure,
  describeUndoResult,
  type CombineToastPlan,
} from '../../pages/libraryCombineToast';
import {
  monotonicProgress,
  outcomeHeadline,
  outcomeSeverity,
  type PurgeProgress,
} from '../../utils/purgeSummary';
import {
  loadTrackedOps,
  saveTrackedOps,
  type TrackedKind,
  type TrackedOpDescriptor,
} from '../../utils/trackedOperations';

/** Shown wherever a merge or combine is blocked because its books are in flight. */
export const BOOK_OP_PENDING_REASON =
  'A merge or combine that includes some of these books is still running. Wait for it to finish.';

/** One tracked operation as the UI sees it. */
export interface TrackedOpView {
  descriptor: TrackedOpDescriptor;
  progress: PurgeProgress | null;
  /** True while polls are failing: the operation may well still be running. */
  stillChecking: boolean;
}

/** What a listener hears when a tracked operation ends (or an undo of a combine ran). */
export interface TrackedOpEnd {
  descriptor: TrackedOpDescriptor;
  kind: TrackedKind | 'undo';
  /** the operation's terminal status; null when it is not known (lost) or for a purge */
  status: string | null;
  errorMessage?: string | null;
  /** the stored result: a merge or combine result object, or the PurgeResult of a purge */
  result: unknown;
  /** why the outcome could not be read (purge) or the operation was lost; null otherwise */
  error: string | null;
  /** the server no longer knows the operation, or this session may not read it */
  lost: boolean;
}

export interface TrackOptions {
  /**
   * Asked when a purge's outcome could not be read: true says something else
   * (an open dialog) already shows the error, so no toast repeats it. Not
   * persisted: after a reload no dialog is open.
   */
  suppressErrorToast?: () => boolean;
}

export interface OperationTracker {
  /** Ids of the books a running merge or combine covers. */
  pendingIds: ReadonlySet<string>;
  /** True when any of `ids` is covered by a running operation. */
  anyPending: (ids: readonly string[]) => boolean;
  /** The running operations, by operation id. */
  ops: ReadonlyMap<string, TrackedOpView>;
  /**
   * Follows `desc.operationId` until it ends, whichever page is showing: the
   * books stay locked meanwhile, the outcome is toasted, and the set survives a
   * reload. Tracking an id that is already followed does nothing (a second
   * track cannot release the books early, nor start a second follower).
   */
  track: (desc: Omit<TrackedOpDescriptor, 'startedAt'>, opts?: TrackOptions) => void;
  /** Hears every end. Returns the unsubscribe. */
  subscribe: (listener: (end: TrackedOpEnd) => void) => () => void;
}

const OperationTrackerContext = createContext<OperationTracker | null>(null);

/** Returns the app-level operation tracker. Must be inside OperationTrackerProvider. */
// eslint-disable-next-line react-refresh/only-export-components
export function useOperationTracker(): OperationTracker {
  const ctx = useContext(OperationTrackerContext);
  if (!ctx) throw new Error('useOperationTracker must be used inside OperationTrackerProvider');
  return ctx;
}

const asObject = <T extends object>(v: unknown): T | null =>
  v && typeof v === 'object' ? (v as T) : null;

/**
 * Follows the background merge, combine and purge operations for the whole app.
 * It sits next to ToastProvider, above every page, so navigating away from the
 * Library neither stops the follow nor loses the outcome toast, and the in-flight
 * ids (the books whose merge and combine buttons stay disabled) are kept
 * per operation id, mirrored to sessionStorage, and resumed after a reload.
 */
export function OperationTrackerProvider({ children }: { children: ReactNode }): JSX.Element {
  const { toast } = useToast();
  const navigate = useNavigate();
  // The toast and navigate functions change identity between renders of their
  // providers; the long-lived followers read the latest through refs.
  const toastRef = useRef(toast);
  const navigateRef = useRef(navigate);
  useEffect(() => {
    toastRef.current = toast;
    navigateRef.current = navigate;
  });

  const [ops, setOps] = useState<ReadonlyMap<string, TrackedOpView>>(() => new Map());
  // Authoritative copy of the descriptors (state updates are batched; the
  // follower and storage must not wait for a render).
  const descriptors = useRef(new Map<string, TrackedOpDescriptor>());
  const controllers = useRef(new Map<string, AbortController>());
  const trackOpts = useRef(new Map<string, TrackOptions>());
  const listeners = useRef(new Set<(end: TrackedOpEnd) => void>());

  const persist = useCallback(() => saveTrackedOps([...descriptors.current.values()]), []);

  const viewOperation = useCallback(
    (operationId: string) => ({
      label: 'View operation',
      onClick: () => navigateRef.current(`/activity?op=${encodeURIComponent(operationId)}`),
    }),
    []
  );

  const emit = useCallback((end: TrackedOpEnd) => {
    for (const l of [...listeners.current]) {
      try {
        l(end);
      } catch (e) {
        console.error('Operation listener failed', e);
      }
    }
  }, []);

  // The combine toast's Undo/Retry actions call back into the latest undo.
  const undoRef = useRef<(operationId: string) => Promise<void>>(async () => {});
  const undoCombine = useCallback(
    async (operationId: string) => {
      const withAction = (plan: CombineToastPlan) =>
        plan.undoOperationId
          ? {
              label: plan.actionLabel ?? 'Undo',
              onClick: () => void undoRef.current(plan.undoOperationId!),
            }
          : undefined;
      let changed = true;
      try {
        const result = await api.revertOperation(operationId);
        const plan = describeUndoResult(result, operationId);
        toastRef.current(plan.message, plan.severity, withAction(plan));
      } catch (error) {
        console.error('Failed to undo combine:', error);
        const plan = describeUndoFailure(error, operationId);
        toastRef.current(plan.message, plan.severity, withAction(plan));
        changed = false;
      }
      if (changed) {
        emit({
          descriptor: {
            operationId,
            kind: 'combine',
            bookIds: [],
            startedAt: Date.now(),
          },
          kind: 'undo',
          status: null,
          result: null,
          error: null,
          lost: false,
        });
      }
    },
    [emit]
  );
  useEffect(() => {
    undoRef.current = undoCombine;
  });

  const lostMessage = (id: string, why: string) =>
    `Lost track of operation ${id}: ${why} What it did is unknown; see Activity.`;

  const finish = useCallback(
    (desc: TrackedOpDescriptor, end: Omit<TrackedOpEnd, 'descriptor' | 'kind'>) => {
      const full: TrackedOpEnd = { descriptor: desc, kind: desc.kind, ...end };
      const id = desc.operationId;
      if (end.lost) {
        toastRef.current(
          lostMessage(id, end.error ?? 'the server could not tell.'),
          'warning',
          viewOperation(id)
        );
      } else if (desc.kind === 'merge') {
        const plan = describeMergeOutcome({
          status: end.status ?? '',
          errorMessage: end.errorMessage,
          result: asObject<BookMergeOpResult>(end.result),
          requested: desc.requested ?? Math.max(0, desc.bookIds.length - 1),
          titles: desc.titles,
        });
        toastRef.current(plan.message, plan.severity, viewOperation(id));
      } else if (desc.kind === 'combine') {
        const plan = describeCombineOutcome({
          status: end.status ?? '',
          errorMessage: end.errorMessage,
          result: asObject<BookCombineOpResult>(end.result),
          operationId: id,
        });
        // One action per toast: Undo when there is something to revert,
        // otherwise a link to the operation.
        const undoId = plan.undoOperationId;
        toastRef.current(
          plan.message,
          plan.severity,
          undoId
            ? { label: plan.actionLabel ?? 'Undo', onClick: () => void undoRef.current(undoId) }
            : viewOperation(id)
        );
      } else {
        const r = asObject<api.PurgeResult>(end.result);
        if (r) {
          toastRef.current(outcomeHeadline(r), outcomeSeverity(r), viewOperation(id));
        } else if (!trackOpts.current.get(id)?.suppressErrorToast?.()) {
          // A refusal or an unreadable outcome is shown ONCE: an open dialog
          // says it itself; only without one does it need a toast.
          toastRef.current(
            end.error ?? 'The purge ended without a readable result.',
            'error',
            viewOperation(id)
          );
        }
      }
      descriptors.current.delete(id);
      controllers.current.delete(id);
      trackOpts.current.delete(id);
      persist();
      setOps((prev) => {
        const next = new Map(prev);
        next.delete(id);
        return next;
      });
      emit(full);
    },
    [emit, persist, viewOperation]
  );

  const follow = useCallback(
    async (desc: TrackedOpDescriptor, signal: AbortSignal) => {
      const id = desc.operationId;
      const label = desc.kind;
      const onProgress = (p: PurgeProgress) => {
        if (signal.aborted) return;
        setOps((prev) => {
          const cur = prev.get(id);
          if (!cur) return prev;
          return new Map(prev).set(id, {
            ...cur,
            progress: monotonicProgress(cur.progress, p),
            stillChecking: false,
          });
        });
      };
      const onTrouble = (e: unknown) => {
        if (signal.aborted) return;
        console.error(`Lost contact while following the ${label} operation`, e);
        setOps((prev) => {
          const cur = prev.get(id);
          return cur ? new Map(prev).set(id, { ...cur, stillChecking: true }) : prev;
        });
        toastRef.current(
          `Still checking on the ${label}: ${describeFollowTrouble(e)}. It may still be running${
            desc.kind === 'purge' ? '' : ', so its books stay locked until it ends'
          }.`,
          'warning',
          viewOperation(id)
        );
      };
      try {
        if (desc.kind === 'purge') {
          try {
            const result = await api.awaitPurgeResult(
              id,
              (p) => onProgress({ done: p.done, total: p.total, message: p.message }),
              { signal, onTrouble, requestTimeoutMs: OPERATION_POLL_TIMEOUT_MS }
            );
            if (signal.aborted) return;
            finish(desc, { status: null, result, error: null, lost: false });
          } catch (e) {
            if (signal.aborted || isAbortError(e)) return;
            if (isOperationLostError(e)) {
              finish(desc, { status: null, result: null, error: e.message, lost: true });
            } else {
              finish(desc, {
                status: null,
                result: null,
                error: e instanceof Error && e.message ? e.message : 'Something went wrong.',
                lost: false,
              });
            }
          }
          return;
        }
        const done = await followOperation(
          (sig) =>
            api.pollOperationV2(
              id,
              (o) =>
                onProgress({
                  done: o.progress_current ?? 0,
                  total: o.progress_total ?? 0,
                  message: o.progress_message ?? '',
                }),
              1000,
              { signal: sig, requestTimeoutMs: OPERATION_POLL_TIMEOUT_MS }
            ),
          { signal, onTrouble }
        );
        if (signal.aborted) return;
        // A failed or cancelled operation stores its result too, so it is
        // fetched for every ended status; a fetch error leaves it null and
        // the outcome wording then says what is unknown, never that it
        // succeeded.
        let result: unknown = null;
        try {
          const { result_data } = await api.getOperationResult(id);
          result = asObject<object>(result_data);
        } catch (e) {
          console.warn(`Could not read the ${label} operation result`, e);
        }
        if (signal.aborted) return;
        finish(desc, {
          status: done.status,
          errorMessage: done.error_message,
          result,
          error: null,
          lost: false,
        });
      } catch (e) {
        if (signal.aborted || isAbortError(e)) return;
        if (isOperationLostError(e)) {
          finish(desc, { status: null, result: null, error: e.message, lost: true });
          return;
        }
        // Unexpected: say so rather than hold the books for good.
        console.error('Background operation tracking failed', e);
        finish(desc, {
          status: null,
          result: null,
          error: e instanceof Error ? e.message : 'unexpected error.',
          lost: true,
        });
      }
    },
    [finish, viewOperation]
  );

  const start = useCallback(
    (desc: TrackedOpDescriptor) => {
      const id = desc.operationId;
      if (controllers.current.has(id)) return;
      const ac = new AbortController();
      controllers.current.set(id, ac);
      descriptors.current.set(id, desc);
      persist();
      setOps((prev) =>
        new Map(prev).set(id, { descriptor: desc, progress: null, stillChecking: false })
      );
      void follow(desc, ac.signal);
    },
    [follow, persist]
  );

  const track = useCallback(
    (desc: Omit<TrackedOpDescriptor, 'startedAt'>, opts?: TrackOptions) => {
      if (controllers.current.has(desc.operationId)) return;
      if (opts) trackOpts.current.set(desc.operationId, opts);
      start({ ...desc, startedAt: Date.now() });
    },
    [start]
  );

  // Resume what an earlier page load left running, and stop following only
  // when the whole app goes away.
  useEffect(() => {
    const live = controllers.current;
    const known = descriptors.current;
    for (const d of loadTrackedOps()) start(d);
    return () => {
      for (const ac of live.values()) ac.abort();
      live.clear();
      known.clear();
    };
  }, [start]);

  const subscribe = useCallback((listener: (end: TrackedOpEnd) => void) => {
    listeners.current.add(listener);
    return () => {
      listeners.current.delete(listener);
    };
  }, []);

  const value = useMemo<OperationTracker>(() => {
    const ids = new Set<string>();
    for (const v of ops.values()) {
      if (v.descriptor.kind === 'purge') continue;
      for (const b of v.descriptor.bookIds) ids.add(b);
    }
    return {
      pendingIds: ids,
      anyPending: (list) => list.some((b) => ids.has(b)),
      ops,
      track,
      subscribe,
    };
  }, [ops, track, subscribe]);

  return (
    <OperationTrackerContext.Provider value={value}>{children}</OperationTrackerContext.Provider>
  );
}
