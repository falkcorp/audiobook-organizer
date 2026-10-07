// file: web/src/hooks/useScopedTagFacets.test.ts
// version: 1.0.0
// guid: 5e2a8c4d-7f1b-4d93-b6a0-3c9e1f7d2b46
// last-edited: 2026-10-06

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../services/api';
import { SCOPED_TAG_FACETS_DEBOUNCE_MS, useScopedTagFacets } from './useScopedTagFacets';

vi.mock('../services/api', () => ({
  getScopedTagFacets: vi.fn(),
}));

const flush = async () => {
  await act(async () => {
    vi.advanceTimersByTime(SCOPED_TAG_FACETS_DEBOUNCE_MS + 1);
  });
  await act(async () => {
    await Promise.resolve();
  });
};

describe('useScopedTagFacets', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.mocked(api.getScopedTagFacets).mockReset();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('fetches with the list options and refetches when the query changes', async () => {
    vi.mocked(api.getScopedTagFacets).mockImplementation(async (opts) => ({
      tags:
        opts?.search === 'dune'
          ? [{ tag: 'scifi', count: 2 }]
          : [
              { tag: 'fantasy', count: 9 },
              { tag: 'scifi', count: 4 },
            ],
      total: opts?.search === 'dune' ? 2 : 13,
    }));

    const { result, rerender } = renderHook(
      ({ opts }: { opts: api.BookListOptions }) => useScopedTagFacets(opts),
      { initialProps: { opts: { isPrimaryVersion: true } as api.BookListOptions } }
    );
    expect(result.current.tags).toBeNull();
    await flush();
    expect(api.getScopedTagFacets).toHaveBeenCalledTimes(1);
    expect(result.current.tags).toEqual([
      { tag: 'fantasy', count: 9 },
      { tag: 'scifi', count: 4 },
    ]);

    // An equal-but-new options object does not refetch.
    rerender({ opts: { isPrimaryVersion: true } });
    await flush();
    expect(api.getScopedTagFacets).toHaveBeenCalledTimes(1);

    // A new query does, with the same predicate the list sends.
    rerender({ opts: { isPrimaryVersion: true, search: 'dune', tags: ['scifi'] } });
    await flush();
    expect(api.getScopedTagFacets).toHaveBeenCalledTimes(2);
    const sent = vi.mocked(api.getScopedTagFacets).mock.calls[1][0];
    expect(sent?.search).toBe('dune');
    expect(sent?.tags).toEqual(['scifi']);
    expect(sent?.isPrimaryVersion).toBe(true);
    expect(result.current.tags).toEqual([{ tag: 'scifi', count: 2 }]);
    expect(result.current.total).toBe(2);
  });

  it('debounces a burst of changes into one request', async () => {
    vi.mocked(api.getScopedTagFacets).mockResolvedValue({ tags: [], total: 0 });
    const { rerender } = renderHook(
      ({ opts }: { opts: api.BookListOptions }) => useScopedTagFacets(opts),
      { initialProps: { opts: { search: 'd' } as api.BookListOptions } }
    );
    rerender({ opts: { search: 'du' } });
    rerender({ opts: { search: 'dun' } });
    rerender({ opts: { search: 'dune' } });
    await flush();
    expect(api.getScopedTagFacets).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.getScopedTagFacets).mock.calls[0][0]?.search).toBe('dune');
  });

  it('never applies an answer for a superseded query', async () => {
    let resolveFirst: (v: api.ScopedTagFacets) => void = () => {};
    vi.mocked(api.getScopedTagFacets)
      .mockImplementationOnce(
        () =>
          new Promise((res) => {
            resolveFirst = res;
          })
      )
      .mockResolvedValueOnce({ tags: [{ tag: 'new', count: 1 }], total: 1 });

    const { result, rerender } = renderHook(
      ({ opts }: { opts: api.BookListOptions }) => useScopedTagFacets(opts),
      { initialProps: { opts: { search: 'old' } as api.BookListOptions } }
    );
    await flush();
    const firstSignal = vi.mocked(api.getScopedTagFacets).mock.calls[0][0]?.signal;
    rerender({ opts: { search: 'new' } });
    expect(firstSignal?.aborted).toBe(true);
    await flush();
    await act(async () => {
      resolveFirst({ tags: [{ tag: 'stale', count: 99 }], total: 99 });
      await Promise.resolve();
    });
    expect(result.current.tags).toEqual([{ tag: 'new', count: 1 }]);
  });
});
