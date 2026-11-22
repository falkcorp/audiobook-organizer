// file: web/src/modules/books/activity/activityBindings.tsx
// version: 0.1.0
// guid: e6c04a92-7f31-4d85-a2b9-48d1f5e7c36a
// last-edited: 2026-11-22

import { BatchActivityEntry } from '../../../components/BatchActivityEntry';
import { operationDisplayName } from '../../../components/layout/operationsFormat';
import TrashCleanupResultPanel from '../../../components/operations/TrashCleanupResultPanel';
import type { ActivityBindings } from '../../../components/activity/activityFrameTypes';
import {
  compactActivityLog,
  fetchActivity,
  fetchActivitySources,
} from '../../../services/activityApi';
import * as api from '../../../services/api';
import { getUndoPreflight } from '../../../services/versionApi';
import { tagChipProps } from '../../../utils/activityTagColors';
import { isInterrupted, isRetryable, isTerminal } from '../../../utils/operationPolling';
import {
  describeRevertResult,
  describeUndoPreflight,
  isUndoRefused,
} from '../../../utils/revertResult';
import { isTrashCleanupDef } from '../../../utils/trashCleanupResult';
import { BOOKS_ACTIVITY_PANES } from './activityPanes';
import { ACTIVITY_EVENT_TYPES } from './eventTypes';
import { useBooksActivityFeed } from './useActivityFeed';

/**
 * Everything the Activity frame takes from the audiobook module, bound to the legacy
 * services. One module-level object: the frame keys its fetch callbacks on these
 * members, so a fresh object per render would refetch on every render. Each member
 * defers to the service at call time (an arrow, not the function itself) so a
 * replaced service is still the one called.
 *
 * F9-03 replaces the services with the shell's native ones and the object with the
 * books module's `activityPanes` and `taskKinds` contributions.
 */
export const booksActivityBindings: ActivityBindings = {
  useFeed: useBooksActivityFeed,
  activity: {
    fetch: (filter, options) => fetchActivity(filter, options),
    fetchSources: (filter, options) => fetchActivitySources(filter, options),
    compact: (days) => compactActivityLog(days),
  },
  operations: {
    cancel: (id) => api.cancelOperation(id),
    discard: (id) => api.discardOperation(id),
    clearStale: () => api.clearStaleOperations(),
    retry: (id) => api.retryOperation(id),
    finish: (id) => api.finishOperation(id),
    logs: (id) => api.getOperationLogs(id),
  },
  status: { isTerminal, isInterrupted, isRetryable },
  undo: {
    offers: (entry) => entry.type === 'organize_completed' || entry.type === 'metadata_applied',
    preflight: async (operationId) => describeUndoPreflight(await getUndoPreflight(operationId)),
    isRefused: isUndoRefused,
    revert: async (operationId) => describeRevertResult(await api.revertOperation(operationId)),
  },
  nameOf: operationDisplayName,
  tagChip: tagChipProps,
  eventTypes: ACTIVITY_EVENT_TYPES,
  bookHref: (bookId) => `/library/${bookId}`,
  renderBatchedEntry: (entry, tierColor) => (
    <BatchActivityEntry entry={entry} tierColor={tierColor} />
  ),
  renderOpDetails: (op) =>
    isTrashCleanupDef(op.def_id) && isTerminal(op.status) ? (
      <TrashCleanupResultPanel opId={op.id} />
    ) : null,
  panes: BOOKS_ACTIVITY_PANES,
};
