// file: web/src/hooks/usePurgeRun.ts
// version: 1.1.0
// guid: c41e7a92-5b08-4d3f-9a6e-2f8d0b7c1e35
// last-edited: 2026-10-22

import { useCallback, useEffect, useRef, useState } from 'react';
import * as api from '../services/api';
import { monotonicProgress, type PurgeProgress } from '../utils/purgeSummary';

export interface PurgeRunState {
  phase: 'idle' | 'running' | 'done' | 'error';
  progress: PurgeProgress | null;
  /** set when phase is 'done' (also for a run that stopped early) */
  result: api.PurgeResult | null;
  /** set when phase is 'error': the run's outcome could not be read */
  error: string | null;
  /** the purge operation's id, once the server has queued it (null before, and when it refused) */
  operationId: string | null;
}

/** How a run ended: with a result (possibly stopped early), or without one. */
export type PurgeRunEnd =
  | { result: api.PurgeResult; error: null; operationId: string }
  | { result: null; error: string; operationId: string | null };

export interface PurgeRunController {
  state: PurgeRunState;
  running: boolean;
  start: (deleteFiles: boolean) => Promise<void>;
  /** Back to idle; ignored while a run is in progress. */
  reset: () => void;
}

const IDLE: PurgeRunState = {
  phase: 'idle',
  progress: null,
  result: null,
  error: null,
  operationId: null,
};

function messageOf(e: unknown): string {
  return e instanceof Error && e.message ? e.message : 'Something went wrong.';
}

/**
 * Owns one purge run, independent of the dialog that started it. The page
 * calls this hook, so closing (unmounting) the dialog mid-run neither stops the
 * polling nor loses the outcome: `running` stays true until the operation
 * ends, and `onFinished` fires then, whether or not the dialog is open. The
 * progress counters only ever move forward.
 */
export function usePurgeRun(
  onFinished: (end: PurgeRunEnd) => void,
  /** fires once the server has queued the purge (it is then running in the background) */
  onStarted?: (operationId: string) => void
): PurgeRunController {
  const [state, setState] = useState<PurgeRunState>(IDLE);
  const abortRef = useRef<AbortController | null>(null);
  const runningRef = useRef(false);
  const onFinishedRef = useRef(onFinished);
  const onStartedRef = useRef(onStarted);
  useEffect(() => {
    onFinishedRef.current = onFinished;
    onStartedRef.current = onStarted;
  });

  // Only the page going away stops the polling.
  useEffect(() => () => abortRef.current?.abort(), []);

  const start = useCallback(async (deleteFiles: boolean) => {
    if (runningRef.current) return;
    runningRef.current = true;
    const ac = new AbortController();
    abortRef.current = ac;
    let operationId: string | null = null;
    setState({ ...IDLE, phase: 'running' });
    try {
      const handle = await api.purgeSoftDeletedBooks(deleteFiles);
      operationId = handle.id;
      setState((s) => (s.phase === 'running' ? { ...s, operationId: handle.id } : s));
      onStartedRef.current?.(handle.id);
      const result = await api.awaitPurgeResult(
        handle.id,
        (p) => {
          if (ac.signal.aborted) return;
          setState((s) =>
            s.phase === 'running' ? { ...s, progress: monotonicProgress(s.progress, p) } : s
          );
        },
        { signal: ac.signal }
      );
      if (ac.signal.aborted) return;
      setState({ phase: 'done', progress: null, result, error: null, operationId: handle.id });
      onFinishedRef.current({ result, error: null, operationId: handle.id });
    } catch (e) {
      if (ac.signal.aborted) return;
      const error = messageOf(e);
      setState({ phase: 'error', progress: null, result: null, error, operationId });
      onFinishedRef.current({ result: null, error, operationId });
    } finally {
      runningRef.current = false;
    }
  }, []);

  const reset = useCallback(() => {
    if (runningRef.current) return;
    setState(IDLE);
  }, []);

  return { state, running: state.phase === 'running', start, reset };
}
