// file: web/src/components/activity/coreRunStatus.test.ts
// version: 0.1.0
// guid: a82e9d7f-bc4d-483d-b3b7-8d32147f2898
// last-edited: 2026-11-22

import { describe, expect, it } from 'vitest';
import { TASK_RUN_STATES, type TaskRunWire } from '@mediacore/shell';
import { isInterrupted, isRetryable, isTerminal } from '../../utils/operationPolling';
import { coreActivityStatus } from './coreRunStatus';
import { taskRunToActivityRow } from './taskRunRow';

// seams.d/F9-05-activity.md, "Legacy status to core state", one legacy status per core state.
const LEGACY_OF: Record<(typeof TASK_RUN_STATES)[number], string> = {
  queued: 'queued',
  waiting_deps: 'queued',
  running: 'running',
  stopping: 'running',
  succeeded: 'completed',
  failed: 'failed',
  timed_out: 'failed',
  canceled: 'canceled',
  interrupted: 'interrupted',
  interrupted_quiesced: 'interrupted_quiesced',
  interrupted_dropped: 'interrupted_dropped',
  awaiting_decision: 'interrupted_ask',
};

describe('coreActivityStatus', () => {
  it('maps every core state (the table has one row per TASK_RUN_STATES value)', () => {
    expect(Object.keys(LEGACY_OF).sort()).toEqual([...TASK_RUN_STATES].sort());
  });

  describe.each(TASK_RUN_STATES.map((s) => [s, LEGACY_OF[s]] as const))('%s (legacy %s)', (state, legacy) => {
    it('isTerminal equals the legacy answer', () => {
      expect(coreActivityStatus.isTerminal(state)).toBe(isTerminal(legacy));
    });
    it('isInterrupted equals the legacy answer', () => {
      expect(coreActivityStatus.isInterrupted(state)).toBe(isInterrupted(legacy));
    });
    it('isRetryable equals the legacy answer', () => {
      expect(coreActivityStatus.isRetryable(state)).toBe(isRetryable(legacy));
    });
    it('the six literal methods equal the legacy literal comparisons', () => {
      expect(coreActivityStatus.isQueued(state)).toBe(legacy === 'queued');
      expect(coreActivityStatus.isSuccess(state)).toBe(legacy === 'completed');
      expect(coreActivityStatus.isFailure(state)).toBe(legacy === 'failed');
      expect(coreActivityStatus.isCanceled(state)).toBe(legacy === 'canceled');
      expect(coreActivityStatus.isRunning(state)).toBe(legacy === 'running');
      expect(coreActivityStatus.isAwaitingDecision(state)).toBe(legacy === 'interrupted_ask');
    });
  });

  it('treats an unknown state as live, not terminal and not retryable', () => {
    expect(coreActivityStatus.isTerminal('future_state')).toBe(false);
    expect(coreActivityStatus.isRetryable('future_state')).toBe(false);
  });
});

describe('taskRunToActivityRow', () => {
  const run: TaskRunWire = {
    id: 'run-robin',
    kind: 'books.maintenance.batman-sweep',
    mode: 'live',
    state: 'awaiting_decision',
    held: false,
    actor: { user_id: 'bruce-wayne' },
    trigger: 'manual',
    parent: 'run-alfred',
    created: '2026-11-22T10:00:00Z',
    started: '2026-11-22T10:01:00Z',
    done: 3,
    total: 9,
    current: 'Gotham chapter 4',
  };

  it('carries the core state as status and the frame-only fields', () => {
    expect(taskRunToActivityRow(run)).toEqual({
      id: 'run-robin',
      def_id: run.kind,
      type: run.kind,
      displayName: run.kind,
      status: 'awaiting_decision',
      progress: 3,
      total: 9,
      message: 'Gotham chapter 4',
      current_item: 'Gotham chapter 4',
      startedAt: Date.parse('2026-11-22T10:01:00Z'),
      finishedAt: undefined,
      parent_id: 'run-alfred',
      canFinish: true,
    });
  });

  it('falls back to the creation time, the kind, and null parent and current', () => {
    const q = taskRunToActivityRow(
      { ...run, state: 'queued', started: undefined, parent: undefined, current: undefined },
      () => undefined,
    );
    expect(q.startedAt).toBe(Date.parse(run.created));
    expect(q.displayName).toBe(run.kind);
    expect(q.parent_id).toBeNull();
    expect(q.current_item).toBeNull();
    expect(q.canFinish).toBe(false);
  });

  it('uses the label for the display name', () => {
    expect(taskRunToActivityRow(run, (k) => (k === run.kind ? 'Batman sweep' : undefined)).displayName).toBe('Batman sweep');
  });
});
