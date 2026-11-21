// file: core/web/packages/ui-kit/src/hooks/useAsyncAction.ts
// version: 0.1.0
// guid: 15f944a1-6c10-4a10-8d98-68266626f6b7
// last-edited: 2026-11-21

import { useState, useCallback, useRef, useEffect } from 'react';

interface AsyncActionState {
  loading: boolean;
  error: string | null;
}

interface UseAsyncActionReturn<T> extends AsyncActionState {
  run: (...args: unknown[]) => Promise<T | undefined>;
  clearError: () => void;
}

/**
 * useAsyncAction wraps an async function with loading and error state.
 * Eliminates repeated useState(false) / try-finally loading boilerplate.
 *
 * The returned `run` callback has a STABLE identity across renders, even when
 * `fn` is an inline arrow function. This is critical: callers commonly pass
 * `run` (or a wrapper) as a useEffect dependency, and an unstable identity
 * triggers an infinite render loop that exhausts browser network sockets
 * (ERR_INSUFFICIENT_RESOURCES).
 *
 * @example
 * const { run: handleSave, loading, error } = useAsyncAction(async () => {
 *   await api.saveBook(book);
 * });
 */
export function useAsyncAction<T = void>(
  fn: (...args: unknown[]) => Promise<T>
): UseAsyncActionReturn<T> {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fnRef = useRef(fn);
  useEffect(() => {
    fnRef.current = fn;
  }, [fn]);

  const run = useCallback(
    async (...args: unknown[]): Promise<T | undefined> => {
      setLoading(true);
      setError(null);
      try {
        return await fnRef.current(...args);
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
        return undefined;
      } finally {
        setLoading(false);
      }
    },
    []
  );

  const clearError = useCallback(() => setError(null), []);

  return { loading, error, run, clearError };
}

export type { AsyncActionState, UseAsyncActionReturn };
