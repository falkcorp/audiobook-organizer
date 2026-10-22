// file: web/src/hooks/usePendingBookOps.ts
// version: 1.1.0
// guid: e3a7c915-4b02-4d68-8f1c-6a9d0b2e5f73
// last-edited: 2026-10-22

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

/** Shown wherever a merge or combine is blocked because its books are in flight. */
export const BOOK_OP_PENDING_REASON =
  'A merge or combine that includes some of these books is still running. Wait for it to finish.';

export interface PendingBookOps {
  /** Ids of the books a running merge or combine operation covers. */
  pendingIds: ReadonlySet<string>;
  /** True when any of `ids` is covered by a running operation. */
  anyPending: (ids: readonly string[]) => boolean;
  /**
   * Marks `bookIds` in flight until `work` settles, then releases them
   * whichever way it ends. `work` owns the user-facing outcome and must not
   * settle on a failed poll: a lost connection is not the operation ending, so
   * it keeps the ids in flight and keeps polling until the operation is
   * terminal (see followOperation). The signal aborts when the page goes away,
   * which also releases the ids.
   */
  track: (
    operationId: string,
    bookIds: readonly string[],
    work: (signal: AbortSignal) => Promise<void>
  ) => void;
}

/**
 * The books a background merge or combine is working on. The Library keeps
 * the merge/combine buttons disabled for a selection that includes any of them,
 * so the same books cannot be submitted twice while the first operation runs
 * (the server would queue the second behind the first and then refuse or skip
 * what the first already changed).
 */
export function usePendingBookOps(): PendingBookOps {
  const [byOperation, setByOperation] = useState<ReadonlyMap<string, readonly string[]>>(
    () => new Map()
  );
  const controllers = useRef(new Set<AbortController>());

  // Only the page going away stops the tracked work.
  useEffect(() => {
    const live = controllers.current;
    return () => {
      for (const ac of live) ac.abort();
      live.clear();
    };
  }, []);

  const pendingIds = useMemo(() => {
    const ids = new Set<string>();
    for (const list of byOperation.values()) for (const id of list) ids.add(id);
    return ids as ReadonlySet<string>;
  }, [byOperation]);

  const anyPending = useCallback(
    (ids: readonly string[]) => ids.some((id) => pendingIds.has(id)),
    [pendingIds]
  );

  const track = useCallback(
    (
      operationId: string,
      bookIds: readonly string[],
      work: (signal: AbortSignal) => Promise<void>
    ) => {
      const ac = new AbortController();
      controllers.current.add(ac);
      setByOperation((prev) => new Map(prev).set(operationId, bookIds));
      const release = () => {
        controllers.current.delete(ac);
        setByOperation((prev) => {
          const next = new Map(prev);
          next.delete(operationId);
          return next;
        });
      };
      // `work` reports its own outcome; a throw it did not handle must still
      // release the books.
      work(ac.signal)
        .catch((e) => console.error('Background book operation tracking failed', e))
        .finally(release);
    },
    []
  );

  return { pendingIds, anyPending, track };
}
