// file: web/src/utils/operationPolling.ts
// version: 1.3.0
// guid: 9d8c7b6a-5f4e-3d2c-1b0a-9e8d7c6b5a4f
// last-edited: 2026-08-22

import * as api from '../services/api';

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

  if (timeoutId) clearTimeout(timeoutId);
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
 * isTerminal reports whether an operations-v2 status is final, i.e. the op will
 * never report progress again and a poller must stop.
 *
 * MATCH THE PREFIX, NOT A LIST. The backend mints a whole family of interrupted
 * statuses — interrupted, interrupted_quiesced, interrupted_dropped,
 * interrupted_restart — one per ResumePolicy. Its own v1 mirror function
 * (internal/operations/registry/legacy_op_status.go) used to enumerate them,
 * drifted behind the side that mints them, and left rows stuck at "pending"
 * forever with nothing logged. A poller that enumerates has the same failure:
 * it never stops, and the UI spins on an op that finished.
 */
export function isTerminal(status: string): boolean {
  return (
    ['completed', 'failed', 'canceled'].includes(status) ||
    status === 'interrupted' ||
    status.startsWith('interrupted_')
  );
}
