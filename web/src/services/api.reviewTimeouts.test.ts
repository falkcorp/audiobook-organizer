// file: web/src/services/api.reviewTimeouts.test.ts
// version: 1.0.0
// guid: 7b4e1a06-2c53-4f89-b7d1-6a0e5c38f942
// last-edited: 2026-09-01
//
// Every /review data call must have a deadline.
//
// These assert on BEHAVIOUR, not on the argument: each test drives a fetch that
// never settles and checks that the call actually rejects with ApiTimeoutError.
// Asserting `toHaveBeenCalledWith({ timeoutMs })` would pass just as happily if
// apiFetch ignored the option entirely, which is precisely the failure that
// leaves a reviewer on a permanent spinner.
//
// Each test also asserts the call is STILL PENDING one millisecond before its
// deadline. Without that half, a mutation setting every timeout to 1ms would
// survive -- the calls would all still reject with ApiTimeoutError, just far
// too eagerly to let a legitimately slow page load.

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import { getReviewCount, getReviewItems, getCachedReviewResults, getDedupCandidates } from './api';
import { ApiTimeoutError } from '../utils/apiFetch';

/**
 * A fetch that never resolves on its own and rejects the way a real one does
 * when its signal is aborted. This is the shape apiFetch's timeout path is
 * written against: it catches the AbortError and rethrows ApiTimeoutError.
 */
function hangingFetch() {
  return vi.fn(
    (_url: unknown, init?: { signal?: AbortSignal }) =>
      new Promise((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => {
          reject(Object.assign(new Error('The operation was aborted.'), { name: 'AbortError' }));
        });
      })
  );
}

describe('review route request deadlines', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    global.fetch = hangingFetch() as unknown as typeof fetch;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  /**
   * @param call     the API function under test
   * @param timeout  its expected deadline in ms
   */
  async function assertTimesOutAt(call: () => Promise<unknown>, timeout: number) {
    const promise = call();
    // Attach a catch immediately so the eventual rejection is never unhandled
    // while we are still advancing timers.
    const settled: { state: 'pending' | 'rejected'; err?: unknown } = { state: 'pending' };
    void promise.catch((err: unknown) => {
      settled.state = 'rejected';
      settled.err = err;
    });

    await vi.advanceTimersByTimeAsync(timeout - 1);
    expect(settled.state).toBe('pending');

    await vi.advanceTimersByTimeAsync(2);
    expect(settled.state).toBe('rejected');
    expect(settled.err).toBeInstanceOf(ApiTimeoutError);
  }

  it('getReviewCount times out at 15s', async () => {
    await assertTimesOutAt(() => getReviewCount(), 15_000);
  });

  it('getReviewItems times out at 30s', async () => {
    await assertTimesOutAt(() => getReviewItems({ status: 'pending' }), 30_000);
  });

  it('getDedupCandidates times out at 60s', async () => {
    await assertTimesOutAt(() => getDedupCandidates(), 60_000);
  });

  it('getCachedReviewResults times out at 120s', async () => {
    // Deliberately the most generous: the metadata lane calls this with
    // limit=0, so the server builds book info for every reviewable row.
    await assertTimesOutAt(() => getCachedReviewResults(0, 0), 120_000);
  });

  it('a caller-supplied signal still cancels before the deadline', async () => {
    // The lanes abort their own request on lane switch. Adding a deadline must
    // not have broken that path: it should still reject as an AbortError, NOT
    // as a timeout, so the lanes' `ctrl.signal.aborted` early-return keeps
    // working and no error banner flashes on a lane switch.
    const ctrl = new AbortController();
    const promise = getReviewItems({ status: 'pending' }, { signal: ctrl.signal });
    const settled: { err?: unknown } = {};
    void promise.catch((err: unknown) => {
      settled.err = err;
    });

    ctrl.abort();
    await vi.advanceTimersByTimeAsync(1);

    expect(settled.err).not.toBeInstanceOf(ApiTimeoutError);
    expect((settled.err as Error).name).toBe('AbortError');
  });
});
