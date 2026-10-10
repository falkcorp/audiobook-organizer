// file: web/src/utils/operationPolling.ts
// version: 1.6.0
// guid: 9d8c7b6a-5f4e-3d2c-1b0a-9e8d7c6b5a4f
// last-edited: 2026-10-10

import * as api from '../services/api';
import * as ops from '../generated/ops';

export interface PollOptions {
  intervalMs?: number;
  timeoutMs?: number;
}

export type OperationUpdateCallback = (op: api.Operation) => void;
export type OperationCompleteCallback = (op: api.Operation) => void;
export type OperationErrorCallback = (error: unknown) => void;

/**
 * pollOperation polls an operation status until it reaches a terminal state.
 * Provides progress updates and completion notification.
 * Returns a cleanup function that should be called on component unmount.
 */
export function pollOperation(
  operationId: string,
  { intervalMs = 2000, timeoutMs = 10 * 60 * 1000 }: PollOptions = {},
  onUpdate?: OperationUpdateCallback,
  onComplete?: OperationCompleteCallback,
  onError?: OperationErrorCallback
): () => void {
  const start = Date.now();
  let timeoutId: ReturnType<typeof setTimeout> | null = null;
  let isCleanedUp = false;

  const tick = async () => {
    try {
      const op = await api.getOperationStatus(operationId);
      if (isCleanedUp || !timeoutId) return; // cleanup already called
      onUpdate?.(op);
      if (isTerminal(op.status)) {
        timeoutId = null;
        onComplete?.(op);
        return; // stop polling
      }
      if (Date.now() - start < timeoutMs) {
        if (timeoutId) clearTimeout(timeoutId);
        timeoutId = setTimeout(tick, intervalMs);
      } else {
        timeoutId = null;
        onError?.(new Error('operation polling timed out'));
      }
    } catch (e) {
      if (isCleanedUp) return;
      if (timeoutId) {
        // Only continue polling if timeoutId is still set (cleanup not called)
        onError?.(e);
        if (Date.now() - start < timeoutMs) {
          if (timeoutId) clearTimeout(timeoutId);
          timeoutId = setTimeout(tick, intervalMs);
        } else {
          timeoutId = null;
        }
      }
    }
  };

  // CodeQL js/trivial-conditional (alert #981): timeoutId was just declared
  // `null` above and nothing assigns it before this point, so the guard was
  // always false — genuine dead code, not a false positive. Removed rather
  // than dismissed.
  timeoutId = setTimeout(tick, intervalMs);

  // Return cleanup function to cancel polling
  return () => {
    isCleanedUp = true;
    if (timeoutId) {
      clearTimeout(timeoutId);
      timeoutId = null;
    }
  };
}

/**
 * isTerminal reports whether a poller must stop. It is the generated isSettled
 * (web/src/generated/ops.ts, from internal/operations/state/state.go): the
 * strict terminal statuses plus every interrupted* status, matched by prefix so
 * a resume policy added later is covered the day it is minted. A poller that
 * does not recognise a settled status never stops, and the UI spins on an op
 * that finished.
 */
export function isTerminal(status: string): boolean {
  return ops.isSettled(status);
}

/**
 * isInterrupted reports whether a status belongs to the interrupted family:
 * the legacy bare "interrupted" plus every "interrupted_*". Generated from the
 * same table as the server's registry.IsInterruptedStatus; a Retry on one of
 * these resumes the SAME operation in place rather than starting a new one.
 */
export function isInterrupted(status: string): boolean {
  return ops.isInterrupted(status);
}

/**
 * isRetryable reports whether the Activity page offers Retry for a status:
 * failed, canceled and the interrupted family (the table's retryable column).
 * Every status here is one the server's retry endpoint accepts
 * (isRetryableV2Status); completed is accepted there too but deliberately not
 * offered here.
 */
export function isRetryable(status: string): boolean {
  return ops.isRetryable(status);
}
