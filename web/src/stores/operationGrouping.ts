// file: web/src/stores/operationGrouping.ts
// version: 1.3.1
// guid: 8c4a1f37-2b95-4e60-9d13-6a7fb2e08c54
// last-edited: 2026-10-10

// Moved to @mediacore/shell (F9-05), typed over a structural row so ActiveOperation still fits;
// this path re-exports it until F9-03 repoints the importers.
export {
  byNewestFirst,
  GROUP_IDLE_GAP_MS,
  GROUP_MAX_SPAN_MS,
  groupOperations,
  groupTimestamp,
  MIN_GROUP_SIZE,
  type OperationGroup,
} from '@mediacore/shell';
