// file: web/src/modules/books/activity/useActivityFeed.ts
// version: 0.1.0
// guid: a8f3c6e1-2d59-4b70-9e84-1c7d5a0b3f92
// last-edited: 2026-11-22

import type { ActivityOpsFeed } from '../../../components/activity/activityFrameTypes';
import { useOperationsStore } from '../../../stores/useOperationsStore';
import { useBooksOperationsFeed } from '../layout/operationsFeed';

/**
 * The legacy operations store as the Activity page's feed: the bell's feed (the
 * grouped and raw windows, the load error, the reload) plus the two things only the
 * page reads, the running subset and the latest log line. Read through selectors,
 * never getState, and module-level so the frame's `useFeed` keeps one identity.
 * F9-03 points the page at the shell's store and deletes this.
 */
export function useBooksActivityFeed(): ActivityOpsFeed {
  const { grouped, raw, loadError, reload } = useBooksOperationsFeed();
  const live = useOperationsStore((state) => state.liveOperations);
  const latestLogEvent = useOperationsStore((state) => state.latestLogEvent);
  return { grouped, raw, live, loadError, latestLogEvent, reload };
}
