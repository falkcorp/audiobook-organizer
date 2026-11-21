// file: web/src/modules/books/layout/operationsFeed.ts
// version: 0.1.0
// guid: 4e8d2b61-9a37-4c05-b1f8-d3a6c0e9f572
// last-edited: 2026-11-21

import type { OperationsFeed, RunStatusModel } from '../../../components/layout/OperationsIndicatorFrame';
import { useOperationsStore } from '../../../stores/useOperationsStore';
import { isTerminal } from '../../../utils/operationPolling';

/**
 * The legacy operations store as the bell's feed. Module-level so the frame's
 * `useFeed` keeps one identity. F9-03 points the bell at the shell's store and
 * deletes this.
 */
export function useBooksOperationsFeed(): OperationsFeed {
  const grouped = useOperationsStore((state) => state.groupedOperations);
  const raw = useOperationsStore((state) => state.activeOperations);
  const alerts = useOperationsStore((state) => state.alertOperations);
  const loadError = useOperationsStore((state) => state.loadError);
  const reload = useOperationsStore((state) => state.loadFromServer);
  return { grouped, raw, alerts, loadError, reload };
}

/**
 * What the legacy operations API's statuses mean. isTerminal is the generated
 * isSettled (the strict terminal statuses plus every interrupted* status).
 */
export const booksRunStatus: RunStatusModel = {
  isTerminal,
  isQueued: (status) => status === 'queued',
  isSuccess: (status) => status === 'completed',
  isFailure: (status) => status === 'failed',
};
