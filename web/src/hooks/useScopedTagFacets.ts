// file: web/src/hooks/useScopedTagFacets.ts
// version: 1.0.0
// guid: 4f7b2d9e-6c1a-4e83-9b05-d2a8e6c3f714
// last-edited: 2026-10-06

import { useEffect, useMemo, useState } from 'react';
import * as api from '../services/api';

/**
 * Delay before the facets request fires. The Library's search box is already
 * debounced (300 ms) before its value reaches the list options; this second,
 * shorter wait coalesces a burst of filter/tag/sort changes into one request.
 */
export const SCOPED_TAG_FACETS_DEBOUNCE_MS = 250;

export interface ScopedTagFacetsState {
  /**
   * Tags over the current result set. While the answer for a new query loads,
   * the previous query's answer is returned (so the panel does not flash empty
   * between keystrokes); once the new query has FAILED, null — a failed query
   * never shows another query's chips. Null before the first answer.
   */
  tags: Array<{ tag: string; count: number }> | null;
  /** Number of books the current request matches (null until known). */
  total: number | null;
  /** True while the answer for the current predicate is outstanding. */
  loading: boolean;
  /** The current predicate's error, if its request failed. */
  error: Error | null;
}

interface Answer {
  key: string;
  tags: Array<{ tag: string; count: number }>;
  total: number;
}

/**
 * Tag counts over EVERY book the Library's current list request matches.
 *
 * `options` must be the very object getBooks is sent (see
 * buildLibraryListOptions), so the chips are counted over exactly the list on
 * screen. Refetches whenever that predicate changes — page and page size are
 * not part of it, so paging does not refetch. Sort IS sent (it keys the
 * server's shared search-result cache, so a facets request right after the
 * list loads reuses the list's match set); the server's facet cache ignores
 * it, so a sort change costs a cache hit, not a recount. An in-flight request
 * is aborted when the predicate changes; a stale answer is never applied, and
 * every answer is stored with the predicate it describes.
 */
export function useScopedTagFacets(
  options: api.BookListOptions,
  enabled = true
): ScopedTagFacetsState {
  // Serialized predicate: the effect keys on its VALUE, so a fresh but equal
  // options object (every render) does not refetch.
  const key = useMemo(() => JSON.stringify(options), [options]);
  const [answer, setAnswer] = useState<Answer | null>(null);
  const [failure, setFailure] = useState<{ key: string; error: Error } | null>(null);

  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      // Through Promise.resolve so even a synchronous throw lands in the
      // catch below rather than escaping the timer callback.
      Promise.resolve()
        .then(() => api.getScopedTagFacets({ ...options, signal: controller.signal }))
        .then((res) => {
          if (controller.signal.aborted) return;
          setAnswer({ key, tags: res.tags, total: res.total });
          setFailure(null);
        })
        .catch((err: unknown) => {
          if (controller.signal.aborted) return;
          console.error('Failed to load scoped tag facets:', err);
          setFailure({ key, error: err instanceof Error ? err : new Error(String(err)) });
        });
    }, SCOPED_TAG_FACETS_DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
    // `options` is deliberately read through `key`: depending on the object
    // identity would refetch on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, enabled]);

  if (answer && answer.key === key) {
    return { tags: answer.tags, total: answer.total, loading: false, error: null };
  }
  if (failure && failure.key === key) {
    return { tags: null, total: null, loading: false, error: failure.error };
  }
  return { tags: answer?.tags ?? null, total: answer?.total ?? null, loading: true, error: null };
}
