// file: web/src/hooks/__tests__/useDebouncedSearch.test.ts
// version: 1.0.0
// guid: 9f4c2a71-8d36-4e50-b1a9-7e0c5b3f2d84
// last-edited: 2026-09-01

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useDebouncedSearch } from '../useDebouncedSearch';

describe('useDebouncedSearch', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('starts empty and does not emit the term before the delay elapses', () => {
    const { result, rerender } = renderHook(({ v }) => useDebouncedSearch(v, 250), {
      initialProps: { v: '' },
    });
    expect(result.current).toBe('');

    rerender({ v: 'war' });
    act(() => {
      vi.advanceTimersByTime(249);
    });
    expect(result.current).toBe('');
  });

  it('emits the term once the delay elapses', () => {
    const { result, rerender } = renderHook(({ v }) => useDebouncedSearch(v, 250), {
      initialProps: { v: '' },
    });
    rerender({ v: 'warship' });
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(result.current).toBe('warship');
  });

  it('coalesces a burst of keystrokes into a single settled term', () => {
    const { result, rerender } = renderHook(({ v }) => useDebouncedSearch(v, 250), {
      initialProps: { v: '' },
    });
    for (const v of ['w', 'wa', 'war', 'wars', 'warsh']) {
      rerender({ v });
      act(() => {
        vi.advanceTimersByTime(100); // never long enough to fire
      });
    }
    expect(result.current).toBe('');
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(result.current).toBe('warsh');
  });

  it('clears in the same tick, without waiting out the timer', () => {
    const { result, rerender } = renderHook(({ v }) => useDebouncedSearch(v, 250), {
      initialProps: { v: '' },
    });
    rerender({ v: 'warship' });
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(result.current).toBe('warship');

    // The clear must land immediately: waiting delayMs here would leave the
    // lane showing results for a term no longer in the box.
    rerender({ v: '' });
    act(() => {
      vi.advanceTimersByTime(0);
    });
    expect(result.current).toBe('');
  });
});
