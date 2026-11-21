// file: core/web/packages/shell/src/providers/index.ts
// version: 0.1.0
// guid: 4a716316-e98e-4709-b072-ee4dbc4ab98f
// last-edited: 2026-11-21

// The shell's providers and stores (07 §7.11): the operations model behind the
// operations indicator and the Activity page. AuthProvider, ToastProvider and
// TaskTrackerProvider live in ../auth, ../toast and ../tasks (F9-01).

export {
  byNewestFirst,
  GROUP_IDLE_GAP_MS,
  GROUP_MAX_SPAN_MS,
  groupOperations,
  groupTimestamp,
  MIN_GROUP_SIZE,
  type GroupableOperation,
  type OperationGroup,
} from './operationGrouping';
export {
  createOperationsStore,
  OperationsStoreProvider,
  useOperationsStore,
  useOperationsStoreApi,
  type OperationRow,
  type OperationsState,
  type OperationsStore,
  type OperationsStoreOptions,
} from './operationsStore';
export {
  isAwaitingDecision,
  isInterruptedRunState,
  isLiveRunState,
  isTerminalRunState,
  runTimeMs,
  TASK_RUN_STATES,
  type TaskRunActorWire,
  type TaskRunStateName,
  type TaskRunWire,
} from './taskRuns';
