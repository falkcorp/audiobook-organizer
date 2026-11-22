// file: web/src/components/activity/coreRunStatus.ts
// version: 0.1.0
// guid: b9c0893d-46a9-4c9c-b58f-e275c6e82ecd
// last-edited: 2026-11-22

// Not yet bound (F9-03 prep). What a core task-run state means to the Activity
// frame, expressed as the frame's own status vocabulary, so F9-03 can hand the
// frame the shell store's rows (whose `status` is a core state name) without
// the frame learning core's state list.
//
// Equivalence rule: every answer here equals the legacy answer (utils/
// operationPolling, over generated/ops.ts) for the legacy status the state maps
// to in seams.d/F9-05-activity.md "Legacy status to core state". The shell's
// own shellRunStatus is used where it agrees. It differs on one state:
// awaiting_decision is the legacy interrupted_ask, which the frame treats as
// settled, interrupted and retryable (it sits in the Interrupted section with a
// Finish button), whereas shellRunStatus.isTerminal says false for it.

import {
  isAwaitingDecision,
  isInterruptedRunState,
  shellRunStatus,
  type TaskRunStateName,
} from '@mediacore/shell';
import type { ActivityStatusModel } from './activityFrameTypes';

/** The frame's status model plus the six answers it still reads as legacy literals. */
export interface CoreActivityStatusModel extends ActivityStatusModel {
  /** Legacy 'queued': queued and waiting_deps. */
  isQueued(status: string): boolean;
  /** Legacy 'completed': succeeded. */
  isSuccess(status: string): boolean;
  /** Legacy 'failed': failed and timed_out. */
  isFailure(status: string): boolean;
  /** Legacy 'canceled'. */
  isCanceled(status: string): boolean;
  /** Legacy 'running': running and stopping. */
  isRunning(status: string): boolean;
  /** Legacy 'interrupted_ask': the run waits for a resume, finish or drop decision. */
  isAwaitingDecision(status: string): boolean;
}

const canceled = (status: string): boolean => status === 'canceled';

/** The core-state implementation of the frame's status model. Module-level: one identity. */
export const coreActivityStatus: CoreActivityStatusModel = {
  isTerminal: (status) => shellRunStatus.isTerminal(status) || isAwaitingDecision(status),
  isInterrupted: (status) => isInterruptedRunState(status) || isAwaitingDecision(status),
  // Retry is offered for failed, canceled and the interrupted family; completed is not.
  isRetryable: (status) =>
    shellRunStatus.isFailure(status) ||
    canceled(status) ||
    isInterruptedRunState(status) ||
    isAwaitingDecision(status),
  isQueued: shellRunStatus.isQueued,
  isSuccess: shellRunStatus.isSuccess,
  isFailure: shellRunStatus.isFailure,
  isCanceled: canceled,
  isRunning: (status) => status === 'running' || status === 'stopping',
  isAwaitingDecision,
};

/** Type check only: the state a row carries is one of core's names. */
export type CoreRunStatus = TaskRunStateName;
