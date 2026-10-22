// file: web/src/utils/followOperation.ts
// version: 1.0.0
// guid: 5d2a8f94-1c36-4e70-b8a5-7f0e3c9d6b12
// last-edited: 2026-10-22

import { isAbortError } from './apiFetch';

export interface FollowOptions {
  signal?: AbortSignal;
  /** First wait after a failed poll. Doubles each failure up to maxDelayMs. */
  baseDelayMs?: number;
  maxDelayMs?: number;
  /**
   * Called once, on the first failed poll of a run of failures: the caller
   * says "still checking" here. It is NOT called again until a poll succeeds
   * and then fails again.
   */
  onTrouble?: (error: unknown) => void;
  /** Called when a poll succeeds after one or more failures. */
  onRecovered?: () => void;
  /** Wait between retries; injectable so a test does not sit through backoff. */
  sleep?: (ms: number, signal?: AbortSignal) => Promise<void>;
}

function defaultSleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('aborted', 'AbortError'));
      return;
    }
    const onAbort = () => {
      clearTimeout(t);
      reject(new DOMException('aborted', 'AbortError'));
    };
    const t = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

/**
 * Runs `poll` until it returns, treating any failure as "the operation may
 * still be running": it waits with exponential backoff and tries again, and
 * never gives up (an operation that lost its connection is not an operation
 * that stopped). Only an abort (the page going away) ends it early; that
 * rejects with the AbortError, as `poll` itself would.
 */
export async function followOperation<T>(
  poll: (signal?: AbortSignal) => Promise<T>,
  opts: FollowOptions = {}
): Promise<T> {
  const { signal, baseDelayMs = 2000, maxDelayMs = 30000, sleep = defaultSleep } = opts;
  let failures = 0;
  for (;;) {
    try {
      const value = await poll(signal);
      if (failures > 0) opts.onRecovered?.();
      return value;
    } catch (e) {
      if (signal?.aborted || isAbortError(e)) throw e;
      if (failures === 0) opts.onTrouble?.(e);
      const wait = Math.min(maxDelayMs, baseDelayMs * 2 ** Math.min(failures, 16));
      failures += 1;
      await sleep(wait, signal);
    }
  }
}
