// file: web/src/hooks/usePurgeRun.ts
// version: 1.0.0
// guid: c41e7a92-5b08-4d3f-9a6e-2f8d0b7c1e35
// last-edited: 2026-10-26

import { useCallback, useEffect, useRef, useState } from 'react';
import * as api from '../services/api';
import { useOperationTracker } from '../components/operations/OperationTrackerProvider';
import type { PurgeProgress } from '../utils/purgeSummary';

export interface PurgeRunState {
  phase: 'idle' | 'running' | 'done' | 'error';
  progress: PurgeProgress | null;
  /** set when phase is 'done' (also for a run that stopped early) */
  result: api.PurgeResult | null;
  /** set when phase is 'error': the run's outcome could not be read */
  error: string | null;
  /** the purge operation's id, once the server has queued it (null before, and when it refused) */
  operationId: string | null;
  /**
   * True while polls are failing: the purge may well still be running, so the
   * run stays in progress and keeps polling (it is not an error).
   */
  stillChecking: boolean;
}

/** How a run ended: with a result (possibly stopped early), or without one. */
export type PurgeRunEnd =
  | { result: api.PurgeResult; error: null; operationId: string }
  | { result: null; error: string; operationId: string | null };

export interface PurgeRunController {
  state: PurgeRunState;
  running: boolean;
  /**
   * Starts the purge. `trashedBefore` is the preview's `trashed_before`, passed
   * through as the string the server sent (never re-formatted through Date,
   * which would round its nanoseconds).
   */
  start: (trashedBefore: string, deleteFiles: boolean) => Promise<void>;
  /** Back to idle; ignored while a run is in progress. */
  reset: () => void;
}

const IDLE: PurgeRunState = {
  phase: 'idle',
  progress: null,
  result: null,
  error: null,
  operationId: null,
  stillChecking: false,
};

function messageOf(e: unknown): string {
  return e instanceof Error && e.message ? e.message : 'Something went wrong.';
}

/**
 * The purge run as the page sees it, independent of the dialog that started
 * it AND of the page itself: the operation is followed by the app-level
 * operation tracker (OperationTrackerProvider), so closing the dialog,
 * navigating away from the Library, or reloading the tab neither stops the
 * polling nor loses the outcome toast, and a run that is still going when this
 * hook mounts is picked up (`running` is true). `onFinished` fires when a purge
 * ends while this hook is mounted, whether or not the dialog is open. The
 * progress counters only ever move forward (the tracker keeps them monotonic).
 *
 * The tracker toasts the outcome (and a lost or unreadable one); `suppressErrorToast`
 * lets the page say that an open dialog already shows an unreadable outcome.
 */
export function usePurgeRun(
  onFinished: (end: PurgeRunEnd) => void,
  /** fires once the server has queued the purge (it is then running in the background) */
  onStarted?: (operationId: string) => void,
  /** true while something already shows a failed outcome, so no toast repeats it */
  suppressErrorToast?: () => boolean
): PurgeRunController {
  const tracker = useOperationTracker();
  const [local, setLocal] = useState<PurgeRunState>(IDLE);
  const startingRef = useRef(false);
  const onFinishedRef = useRef(onFinished);
  const onStartedRef = useRef(onStarted);
  const suppressRef = useRef(suppressErrorToast);
  useEffect(() => {
    onFinishedRef.current = onFinished;
    onStartedRef.current = onStarted;
    suppressRef.current = suppressErrorToast;
  });

  // The purge the tracker is following (started here or before a reload).
  let tracked: {
    operationId: string;
    progress: PurgeProgress | null;
    stillChecking: boolean;
  } | null = null;
  for (const v of tracker.ops.values()) {
    if (v.descriptor.kind === 'purge') {
      tracked = {
        operationId: v.descriptor.operationId,
        progress: v.progress,
        stillChecking: v.stillChecking,
      };
      break;
    }
  }

  // How a purge ended while this hook is mounted.
  const { subscribe } = tracker;
  useEffect(
    () =>
      subscribe((end) => {
        if (end.kind !== 'purge') return;
        const operationId = end.descriptor.operationId;
        const result = end.result as api.PurgeResult | null;
        if (result && !end.lost) {
          setLocal({ ...IDLE, phase: 'done', result, operationId });
          onFinishedRef.current({ result, error: null, operationId });
          return;
        }
        const error = end.error ?? 'The purge ended without a readable result.';
        setLocal({ ...IDLE, phase: 'error', error, operationId });
        onFinishedRef.current({ result: null, error, operationId });
      }),
    [subscribe]
  );

  const start = useCallback(
    async (trashedBefore: string, deleteFiles: boolean) => {
      if (startingRef.current) return;
      startingRef.current = true;
      setLocal({ ...IDLE, phase: 'running' });
      try {
        const handle = await api.purgeSoftDeletedBooks(trashedBefore, deleteFiles);
        setLocal((s) => (s.phase === 'running' ? { ...s, operationId: handle.id } : s));
        // Say it started before the tracker starts following (its first poll
        // can already toast "still checking", which must come after).
        onStartedRef.current?.(handle.id);
        tracker.track(
          { operationId: handle.id, kind: 'purge', bookIds: [] },
          { suppressErrorToast: () => suppressRef.current?.() ?? false }
        );
      } catch (e) {
        const error = messageOf(e);
        setLocal({ ...IDLE, phase: 'error', error, operationId: null });
        onFinishedRef.current({ result: null, error, operationId: null });
      } finally {
        startingRef.current = false;
      }
    },
    [tracker]
  );

  const reset = useCallback(() => {
    if (tracked) return;
    setLocal(IDLE);
  }, [tracked]);

  const state: PurgeRunState = tracked
    ? {
        ...IDLE,
        phase: 'running',
        operationId: tracked.operationId,
        progress: tracked.progress,
        stillChecking: tracked.stillChecking,
      }
    : local;

  return { state, running: state.phase === 'running', start, reset };
}
