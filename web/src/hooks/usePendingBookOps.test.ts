// file: web/src/hooks/usePendingBookOps.test.ts
// version: 1.1.0
// guid: 3b9d7e14-a052-4c68-b1f3-8e6a0d2c5f97
// last-edited: 2026-10-22

import { act, renderHook } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { usePendingBookOps } from './usePendingBookOps';
import { followOperation } from '../utils/followOperation';

describe('usePendingBookOps', () => {
  it('holds the books of a running operation and releases them when its work settles', async () => {
    const { result } = renderHook(() => usePendingBookOps());
    let finish: () => void = () => {};
    act(() => {
      result.current.track(
        'op-1',
        ['book-a', 'book-b'],
        () => new Promise<void>((resolve) => (finish = resolve))
      );
    });
    expect(result.current.anyPending(['book-b', 'book-z'])).toBe(true);
    expect(result.current.anyPending(['book-z'])).toBe(false);

    await act(async () => finish());
    expect(result.current.anyPending(['book-a'])).toBe(false);
    expect(result.current.pendingIds.size).toBe(0);
  });

  it('releases the books when the work throws', async () => {
    const { result } = renderHook(() => usePendingBookOps());
    await act(async () => {
      result.current.track('op-1', ['book-a'], async () => {
        throw new Error('poll failed');
      });
    });
    expect(result.current.anyPending(['book-a'])).toBe(false);
  });

  it('keeps two operations apart and aborts the work when the page goes away', async () => {
    const { result, unmount } = renderHook(() => usePendingBookOps());
    const signals: AbortSignal[] = [];
    let finishFirst: () => void = () => {};
    act(() => {
      result.current.track('op-1', ['book-a'], (signal) => {
        signals.push(signal);
        return new Promise<void>((resolve) => (finishFirst = resolve));
      });
      result.current.track('op-2', ['book-b'], (signal) => {
        signals.push(signal);
        return new Promise<void>(() => {});
      });
    });
    await act(async () => finishFirst());
    expect(result.current.anyPending(['book-a'])).toBe(false);
    expect(result.current.anyPending(['book-b'])).toBe(true);
    expect(signals.map((s) => s.aborted)).toEqual([false, false]);
    unmount();
    expect(signals[1].aborted).toBe(true);
  });

  it('keeps the books in flight through failed polls and releases them only at the terminal state', async () => {
    const { result } = renderHook(() => usePendingBookOps());
    let failNext = 3;
    let ended = false;
    const poll = vi.fn(async () => {
      if (failNext > 0) {
        failNext -= 1;
        throw new Error('network down');
      }
      return ended ? 'completed' : 'running';
    });
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    const troubles: unknown[] = [];
    let work: Promise<void> = Promise.resolve();
    act(() => {
      result.current.track('op-1', ['book-a', 'book-b'], async (signal) => {
        work = (async () => {
          let status = '';
          while (status !== 'completed') {
            status = await followOperation(poll, {
              signal,
              onTrouble: (e) => troubles.push(e),
              sleep: () => Promise.resolve(),
            });
            if (status === 'running') await gate;
          }
        })();
        await work;
      });
    });
    // Let the three failures and the first "running" answer go by.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(poll).toHaveBeenCalledTimes(4);
    expect(troubles).toHaveLength(1);
    // Contact was lost three times and the operation is not terminal: the
    // buttons for these books must still be disabled.
    expect(result.current.anyPending(['book-a'])).toBe(true);
    expect(result.current.anyPending(['book-b'])).toBe(true);

    ended = true;
    await act(async () => {
      release();
      await work;
    });
    expect(result.current.anyPending(['book-a', 'book-b'])).toBe(false);
  });
});
