// file: web/src/hooks/__tests__/useDebouncedSearch.test.ts
// version: 1.1.0
// guid: 9f4c2a71-8d36-4e50-b1a9-7e0c5b3f2d84
// last-edited: 2026-09-01

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { serverAnsweredTerm, useDebouncedSearch } from '../useDebouncedSearch';

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

describe('serverAnsweredTerm', () => {
  // Shared by BOTH review lanes, and load-bearing in each: when it wrongly
  // returns false the lane's local filter keeps running over rows the server
  // already selected, discarding every row the server matched on a field the
  // browser cannot see. "total says 12, the list shows 0."
  //
  // The case-fold and trim are the part that breaks in normal use: the lane
  // stores appliedSearch RAW but compares against a lower-cased, trimmed
  // needle, so a reviewer typing "Dune" or "herbert " desynchronises the two.
  // Every lane-level test happens to use clean lowercase terms, so a mutant
  // dropping .trim().toLowerCase() survives all of them.
  it('matches an identical term', () => {
    expect(serverAnsweredTerm('dune', 'dune')).toBe(true);
  });

  it('matches regardless of case, because the server matches case-insensitively', () => {
    expect(serverAnsweredTerm('Dune', 'dune')).toBe(true);
    expect(serverAnsweredTerm('dune', 'DUNE')).toBe(true);
    expect(serverAnsweredTerm('DuNe', 'dUnE')).toBe(true);
  });

  it('matches regardless of surrounding whitespace', () => {
    expect(serverAnsweredTerm('  dune', 'dune  ')).toBe(true);
    expect(serverAnsweredTerm('dune ', ' dune')).toBe(true);
  });

  it('treats empty and whitespace-only as the same unanswered state', () => {
    expect(serverAnsweredTerm('', '')).toBe(true);
    expect(serverAnsweredTerm('   ', '')).toBe(true);
  });

  it('does NOT match a genuinely different term', () => {
    expect(serverAnsweredTerm('dune', 'neuromancer')).toBe(false);
    // A prefix is not an answer: the server has not run for the longer term.
    expect(serverAnsweredTerm('dun', 'dune')).toBe(false);
    expect(serverAnsweredTerm('', 'dune')).toBe(false);
  });
});
