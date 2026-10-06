// file: web/src/hooks/useServerMatchingCount.ts
// version: 1.0.0
// guid: e476b3ef-7227-4d5f-91b9-b462cd2e6240
// last-edited: 2026-10-06

/**
 * useServerMatchingCount: the server's count of the rows a filter-scoped bulk
 * action would act on, for the "Select all N matching" banner and the
 * "N selected (every page)" bar.
 *
 * The list endpoint's `total` is a paging hint (it includes rows the bulk
 * endpoints skip, e.g. pairs naming a deleted book, or decided rows on a list
 * that shows every status). Showing it on the banner while the confirmation
 * dialog shows the server's bulk count put two different numbers on one
 * selection. This hook fetches the bulk count only while it is needed (the
 * caller reports that during render through `sync`) and refetches when
 * `refreshKey` changes, keeping the last known count on screen meanwhile.
 *
 * The confirmation dialog still recounts at confirm time and sends THAT
 * number as expected_total: the filter can move between the banner and the
 * confirm, and the server checks the number the reviewer confirmed.
 */

import { useCallback, useEffect, useMemo, useState } from 'react';

export type ServerMatchingCount =
  | { state: 'idle' }
  | { state: 'counting' }
  | { state: 'ready'; n: number }
  | { state: 'error'; message: string };

export interface UseServerMatchingCount {
  count: ServerMatchingCount;
  /**
   * Report whether the count is needed. Safe to call during render: it only
   * sets state when the answer changed.
   */
  sync: (needed: boolean) => void;
  /** The server count when known, else `fallback` (the list's total). */
  totalOr: (fallback: number) => number;
}

export function useServerMatchingCount(
  fetchCount: (signal: AbortSignal) => Promise<number>,
  refreshKey: unknown
): UseServerMatchingCount {
  const [needed, setNeeded] = useState(false);
  // The last answer the server gave. Set only from the fetch's callbacks;
  // the visible state is derived below, so nothing is set inside the effect
  // body itself.
  const [result, setResult] = useState<ServerMatchingCount | null>(null);

  useEffect(() => {
    if (!needed) return;
    const ctrl = new AbortController();
    Promise.resolve()
      .then(() => fetchCount(ctrl.signal))
      .then(
        (n) => {
          if (ctrl.signal.aborted) return;
          setResult(
            typeof n === 'number'
              ? { state: 'ready', n }
              : { state: 'error', message: 'The server returned no count' }
          );
        },
        (err: unknown) => {
          if (ctrl.signal.aborted) return;
          setResult({
            state: 'error',
            message: err instanceof Error ? err.message : 'Could not count the matching rows',
          });
        }
      );
    return () => ctrl.abort();
  }, [needed, fetchCount, refreshKey]);

  // A known count stays on screen while it is refreshed; before the first
  // answer it is "counting".
  const count = useMemo<ServerMatchingCount>(
    () => (!needed ? { state: 'idle' } : (result ?? { state: 'counting' })),
    [needed, result]
  );

  // Compared before setting: an unconditional set during render would
  // re-render forever. Dropping the need also drops the last answer, so a
  // count taken for one selection is never shown for the next.
  const sync = useCallback(
    (next: boolean) => {
      if (next === needed) return;
      setNeeded(next);
      if (!next) setResult(null);
    },
    [needed]
  );
  const totalOr = useCallback(
    (fallback: number) => (count.state === 'ready' ? count.n : fallback),
    [count]
  );
  return { count, sync, totalOr };
}
