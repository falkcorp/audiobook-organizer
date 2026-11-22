// file: web/src/components/activity/taskRunRow.ts
// version: 0.1.0
// guid: 4ede900e-79fa-4e8b-bd55-9dfeba6019e1
// last-edited: 2026-11-22

// Not yet bound (F9-03 prep). One core task run (the wire shape
// GET /api/core/v1/tasks/runs returns) as the row the Activity frame renders.
// It carries the same fields the shell store's own rows carry (providers/
// operationsStore.tsx toRow, which is not exported), with `status` the core
// state name, plus the two the frame reads beyond a bell row: `current_item`
// and `canFinish`.

import { isAwaitingDecision, runTimeMs, type TaskRunWire } from '@mediacore/shell';
import type { ActivityOpRow } from './activityFrameTypes';

/** `label` names a kind for display (the books taskKinds); the kind itself by default. */
export function taskRunToActivityRow(run: TaskRunWire, label?: (kind: string) => string | undefined): ActivityOpRow {
  const awaiting = isAwaitingDecision(run.state);
  return {
    id: run.id,
    def_id: run.kind,
    type: run.kind,
    displayName: label?.(run.kind) ?? run.kind,
    status: run.state,
    progress: run.done,
    total: run.total,
    message: run.current ?? '',
    current_item: run.current ? run.current : null,
    // A queued run has no start yet; its creation time keeps it orderable.
    startedAt: runTimeMs(run.started) ?? runTimeMs(run.created),
    finishedAt: runTimeMs(run.finished),
    parent_id: run.parent ? run.parent : null,
    canFinish: awaiting,
  };
}
