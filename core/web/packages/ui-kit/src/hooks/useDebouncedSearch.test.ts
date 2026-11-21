// file: core/web/packages/ui-kit/src/hooks/useDebouncedSearch.test.ts
// version: 0.1.0
// guid: 392bae4a-68ad-49db-a0fe-3e2202c88e3e
// last-edited: 2026-11-21

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { serverAnsweredTerm, useDebouncedSearch, useSettledSearch } from './useDebouncedSearch';

describe('useDebouncedSearch', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('starts empty and does not emit the term before the delay elapses', () => {
    const { result, rerender } = renderHook(({ v }) => useDebouncedSearch(v, 250), {
      initialProps: { v: '' },
    });
    expect(result.current).toBe('');

    rerender({ v: 'jok' });
    act(() => {
      vi.advanceTimersByTime(249);
    });
    expect(result.current).toBe('');
  });

  it('emits the term once the delay elapses', () => {
    const { result, rerender } = renderHook(({ v }) => useDebouncedSearch(v, 250), {
      initialProps: { v: '' },
    });
    rerender({ v: 'joker' });
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(result.current).toBe('joker');
  });

  it('coalesces a burst of keystrokes into a single settled term', () => {
    const { result, rerender } = renderHook(({ v }) => useDebouncedSearch(v, 250), {
      initialProps: { v: '' },
    });
    for (const v of ['w', 'wa', 'war', 'wars', 'jokr']) {
      rerender({ v });
      act(() => {
        vi.advanceTimersByTime(100); // never long enough to fire
      });
    }
    expect(result.current).toBe('');
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(result.current).toBe('jokr');
  });

  it('clears in the same tick, without waiting out the timer', () => {
    const { result, rerender } = renderHook(({ v }) => useDebouncedSearch(v, 250), {
      initialProps: { v: '' },
    });
    rerender({ v: 'joker' });
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(result.current).toBe('joker');

    // The clear must land immediately: waiting delayMs here would leave the
    // list showing results for a term no longer in the box.
    rerender({ v: '' });
    act(() => {
      vi.advanceTimersByTime(0);
    });
    expect(result.current).toBe('');
  });
});

describe('serverAnsweredTerm', () => {
  // Load-bearing: when this wrongly returns false the list's local filter keeps
  // running over rows the server already selected, discarding every row the
  // server matched on a field the browser cannot see. "total says 12, the list
  // shows 0."
  //
  // The case-fold and trim are the part that breaks in normal use: a list stores
  // appliedSearch RAW but compares against a lower-cased, trimmed needle, so a
  // user typing "Gordon" or "riddler " desynchronises the two. A mutant dropping
  // .trim().toLowerCase() must not survive.
  it('matches an identical term', () => {
    expect(serverAnsweredTerm('gordon', 'gordon')).toBe(true);
  });

  it('matches regardless of case, because the server matches case-insensitively', () => {
    expect(serverAnsweredTerm('Gordon', 'gordon')).toBe(true);
    expect(serverAnsweredTerm('gordon', 'GORDON')).toBe(true);
    expect(serverAnsweredTerm('GoRdOn', 'gOrDoN')).toBe(true);
  });

  it('matches regardless of surrounding whitespace', () => {
    expect(serverAnsweredTerm('  gordon', 'gordon  ')).toBe(true);
    expect(serverAnsweredTerm('gordon ', ' gordon')).toBe(true);
  });

  it('treats empty and whitespace-only as the same unanswered state', () => {
    expect(serverAnsweredTerm('', '')).toBe(true);
    expect(serverAnsweredTerm('   ', '')).toBe(true);
  });

  it('does NOT match a genuinely different term', () => {
    expect(serverAnsweredTerm('gordon', 'riddler')).toBe(false);
    // A prefix is not an answer: the server has not run for the longer term.
    expect(serverAnsweredTerm('gord', 'gordon')).toBe(false);
    expect(serverAnsweredTerm('', 'gordon')).toBe(false);
  });
});

describe('useDebouncedSearch initial term', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('starts settled on the initial term, so a linked search does not fetch unfiltered first', () => {
    const { result } = renderHook(() => useDebouncedSearch('gordon', 250, 'gordon'));
    expect(result.current).toBe('gordon');
  });
});

describe('useSettledSearch', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('settleNow lands a term at once, without waiting out the delay', () => {
    // The box already holds the term (a pasted link); the timer has not fired.
    const { result } = renderHook(() => useSettledSearch('penguin', 250));
    expect(result.current.settled).toBe('');
    act(() => result.current.settleNow('penguin'));
    expect(result.current.settled).toBe('penguin');
  });

  it('keeps settleNow stable across renders', () => {
    const { result, rerender } = renderHook(({ v }) => useSettledSearch(v, 250), { initialProps: { v: '' } });
    const first = result.current.settleNow;
    rerender({ v: 'bane' });
    expect(result.current.settleNow).toBe(first);
  });
});
