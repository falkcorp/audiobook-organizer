// file: core/web/packages/shell/src/layout/shellOperations.tsx
// version: 0.1.0
// guid: 2a6d9f14-8e37-4c50-b1a3-f7c0e5d82b69
// last-edited: 2026-11-21

// The operations bell over the shell's own store (useOperationsStore) and core's
// task-run states. A host mounts <OperationsStoreProvider> above it and supplies
// what the shell cannot know: how to cancel a run, what the activity dialog
// shows, and any per-row actions. Nothing here names an audiobook route or
// store; the audiobook app binds the same frame to its legacy store instead,
// until the cut-over points it here.

import {
  isInterruptedRunState,
  isTerminalRunState,
  useOperationsStore,
  useOperationsStoreApi,
} from '../providers';
import {
  OperationsIndicatorFrame,
  type OperationsFeed,
  type OperationsIndicatorFrameProps,
  type RunStatusModel,
} from './OperationsIndicatorFrame';

/** The bell's feed from the shell store. Module-level, so the frame's `useFeed` keeps one identity. */
export function useShellOperationsFeed(): OperationsFeed {
  const grouped = useOperationsStore((s) => s.groupedOperations);
  const raw = useOperationsStore((s) => s.activeOperations);
  const alerts = useOperationsStore((s) => s.alertOperations);
  const loadError = useOperationsStore((s) => s.loadError);
  const reload = useOperationsStoreApi().loadFromServer;
  return { grouped, raw, alerts, loadError, reload };
}

/**
 * What core's run states mean to the bell. A settled run is a terminal one or any
 * interrupted* one (the strict terminal set plus the interrupted family: the
 * run is not progressing, so it does not count as in progress). A run still
 * waiting on its dependencies has not started, so it is Pending with the queued
 * ones. timed_out is a failure.
 */
export const shellRunStatus: RunStatusModel = {
  isTerminal: (state) => isTerminalRunState(state) || isInterruptedRunState(state),
  isQueued: (state) => state === 'queued' || state === 'waiting_deps',
  isSuccess: (state) => state === 'succeeded',
  isFailure: (state) => state === 'failed' || state === 'timed_out',
};

export type OperationsIndicatorProps = Omit<OperationsIndicatorFrameProps, 'useFeed' | 'status' | 'nameOf'> & {
  /** A row's name; the store's own (the kind's label, or the kind) by default. */
  nameOf?: OperationsIndicatorFrameProps['nameOf'];
};

/** The shell's operations bell: the frame over the shell store and core's run states. */
export function OperationsIndicator({ nameOf, ...props }: OperationsIndicatorProps) {
  return (
    <OperationsIndicatorFrame
      useFeed={useShellOperationsFeed}
      status={shellRunStatus}
      nameOf={nameOf ?? ((op) => op.displayName?.trim() || op.def_id || op.id)}
      {...props}
    />
  );
}
