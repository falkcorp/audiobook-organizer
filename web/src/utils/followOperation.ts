// file: web/src/utils/followOperation.ts
// version: 1.0.0
// guid: 5d2a8f94-1c36-4e70-b8a5-7f0e3c9d6b12
// last-edited: 2026-10-26

import { isAbortError } from './apiFetch';

/**
 * Per-request deadline for one operation poll. A poll that hangs for longer is
 * a failed poll (retryable), not a request that stalls the follow loop forever.
 */
export const OPERATION_POLL_TIMEOUT_MS = 30000;

/** What a failed poll tells us. */
export type FollowErrorClass =
  /** 404/410: the server no longer knows the operation (pruned, or a different server). */
  | 'gone'
  /** 401/403 or a bounce to a login page: this session may not read the operation. */
  | 'auth'
  /** the request hit its own deadline */
  | 'timeout'
  /** the server answered 5xx (or another unexpected status) */
  | 'server'
  /** no answer at all */
  | 'network';

// Duck-typed on purpose: services/api.ts imports this file, and importing its
// ApiError class back would be a cycle.
function statusOf(e: unknown): number | null {
  const o = e as { name?: string; status?: unknown } | null | undefined;
  return o?.name === 'ApiError' && typeof o.status === 'number' ? o.status : null;
}

/** Sorts a failed poll into the class that decides what to do and what to say. */
export function classifyFollowError(e: unknown): FollowErrorClass {
  const name = (e as { name?: string } | null | undefined)?.name;
  if (name === 'ApiTimeoutError') return 'timeout';
  if (name === 'ApiAuthRedirectError') return 'auth';
  const status = statusOf(e);
  if (status === 404 || status === 410) return 'gone';
  if (status === 401 || status === 403) return 'auth';
  if (status !== null) return 'server';
  return 'network';
}

/**
 * A poll that can never succeed: the operation is unknown to the server, or
 * this session is not allowed to read it. It may well have ended; what it did
 * is not knowable from here, so following stops and the caller says so (and
 * points at the Activity page) instead of polling forever with the books locked.
 */
export class OperationLostError extends Error {
  readonly reason: 'gone' | 'auth';
  readonly cause: unknown;
  constructor(reason: 'gone' | 'auth', cause: unknown) {
    super(
      reason === 'gone'
        ? 'The server no longer has a record of this operation.'
        : 'This session is not allowed to read this operation.'
    );
    this.name = 'OperationLostError';
    this.reason = reason;
    this.cause = cause;
  }
}

export function isOperationLostError(e: unknown): e is OperationLostError {
  return (e as { name?: string } | null | undefined)?.name === 'OperationLostError';
}

/** Why contact with a still-running operation was lost, in words that match the failure. */
export function describeFollowTrouble(e: unknown): string {
  switch (classifyFollowError(e)) {
    case 'timeout':
      return 'the server did not answer in time';
    case 'server': {
      const status = statusOf(e);
      return status === null
        ? 'the server answered with an error'
        : `the server answered ${status}`;
    }
    case 'network':
      return 'the server could not be reached';
    case 'gone':
      return 'the server no longer knows the operation';
    case 'auth':
      return 'this session is not allowed to read it';
  }
}

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
 * Runs `poll` until it returns, treating a network, timeout or 5xx failure as
 * "the operation may still be running": it waits with exponential backoff and
 * tries again, and never gives up on those (an operation that lost its
 * connection is not an operation that stopped). Two things end it early: an
 * abort (the page going away) rejects with the AbortError, as `poll` itself
 * would; and a failure that waiting cannot fix (404/410 unknown operation,
 * 401/403 not allowed) rejects with OperationLostError, so the caller can say
 * the outcome is unknown and release what it was holding.
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
      // 404/410/401/403 never get better by waiting: stop and say the outcome
      // is unknown rather than poll forever with the books held.
      const cls = classifyFollowError(e);
      if (cls === 'gone' || cls === 'auth') throw new OperationLostError(cls, e);
      if (failures === 0) opts.onTrouble?.(e);
      const wait = Math.min(maxDelayMs, baseDelayMs * 2 ** Math.min(failures, 16));
      failures += 1;
      await sleep(wait, signal);
    }
  }
}
