// file: web/src/hooks/usePendingBookOps.test.ts
// version: 1.0.0
// guid: 3b9d7e14-a052-4c68-b1f3-8e6a0d2c5f97
// last-edited: 2026-10-22

import { act, renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { usePendingBookOps } from './usePendingBookOps';

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
});
