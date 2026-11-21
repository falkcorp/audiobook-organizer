// file: core/web/packages/ui-kit/src/hooks/useAsyncAction.test.ts
// version: 0.1.0
// guid: 5c1e7a94-3b60-4d2f-8e17-0a9d4c6b2f31
// last-edited: 2026-11-21

import { act, renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { useAsyncAction } from './useAsyncAction';

function gate<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('useAsyncAction', () => {
  it('is loading while the call is in flight and returns its value', async () => {
    const g = gate<string>();
    const { result } = renderHook(() => useAsyncAction(() => g.promise));
    expect(result.current.loading).toBe(false);
    let out: Promise<string | undefined>;
    act(() => {
      out = result.current.run();
    });
    expect(result.current.loading).toBe(true);
    await act(async () => {
      g.resolve('batarang');
      await out;
    });
    expect(result.current.loading).toBe(false);
    expect(result.current.error).toBeNull();
    await expect(out!).resolves.toBe('batarang');
  });

  it('turns a rejection into error text and undefined, and stops loading', async () => {
    const { result } = renderHook(() => useAsyncAction(async () => { throw new Error('Arkham is full'); }));
    let out: string | undefined = 'unset';
    await act(async () => {
      out = await result.current.run();
    });
    expect(out).toBeUndefined();
    expect(result.current.error).toBe('Arkham is full');
    expect(result.current.loading).toBe(false);
  });

  it('stringifies a thrown non-Error', async () => {
    const { result } = renderHook(() => useAsyncAction(async () => { throw 'riddle'; }));
    await act(async () => {
      await result.current.run();
    });
    expect(result.current.error).toBe('riddle');
  });

  it('clears the previous error when it runs again, and clearError clears it by hand', async () => {
    let fail = true;
    const { result } = renderHook(() => useAsyncAction(async () => { if (fail) throw new Error('nope'); return 1; }));
    await act(async () => { await result.current.run(); });
    expect(result.current.error).toBe('nope');
    fail = false;
    await act(async () => { await result.current.run(); });
    expect(result.current.error).toBeNull();
    fail = true;
    await act(async () => { await result.current.run(); });
    act(() => result.current.clearError());
    expect(result.current.error).toBeNull();
  });

  it('passes its arguments through', async () => {
    const seen: unknown[][] = [];
    const { result } = renderHook(() => useAsyncAction(async (...a: unknown[]) => { seen.push(a); }));
    await act(async () => { await result.current.run('robin', 3); });
    expect(seen).toEqual([['robin', 3]]);
  });

  // The identity guarantee the hook exists for: run is a useEffect dependency in
  // callers, and an unstable one loops the render until the browser runs out of sockets.
  it('keeps run stable across renders, yet calls the latest function', async () => {
    const calls: string[] = [];
    const { result, rerender } = renderHook(({ tag }) => useAsyncAction(async () => { calls.push(tag); }), {
      initialProps: { tag: 'first' },
    });
    const run = result.current.run;
    rerender({ tag: 'second' });
    expect(result.current.run).toBe(run);
    await act(async () => { await run(); });
    expect(calls).toEqual(['second']);
  });
});
