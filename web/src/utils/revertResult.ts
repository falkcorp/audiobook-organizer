// file: web/src/utils/revertResult.ts
// version: 1.2.0
// guid: 05f380f7-79a1-4953-8339-85979484c6a2
// last-edited: 2026-09-12

import type { RevertOperationResult } from '../services/api';
import type { UndoConflictReport } from '../services/versionApi';

const capitalize = (s: string): string => s.charAt(0).toUpperCase() + s.slice(1);

/**
 * The line to show the user after an operation revert returns 200. A partial
 * result (some rows failed or cannot be undone automatically) must never read
 * as a success, so the server's own summary is shown whenever it is present.
 */
export function describeRevertResult(result: Partial<RevertOperationResult> | null | undefined): string {
  if (result?.partial) {
    if (result.message) return capitalize(result.message);
    return `Operation only partially reverted: ${result.restored ?? 0} of ${result.total ?? 0} changes restored`;
  }
  if (result?.message) return capitalize(result.message);
  return 'Operation reverted';
}

const formatTypeCounts = (counts: Record<string, number> | undefined): string =>
  Object.entries(counts ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([type, n]) => `${n} ${type} ${n === 1 ? 'row' : 'rows'}`)
    .join(', ');

/**
 * The confirmation to show before calling the revert endpoint, built from the
 * undo preflight. The count offered is the rows the server will actually try
 * to restore (safe plus conflicting rows); record-only rows are named
 * separately, and when nothing is restorable canUndo is false so no Undo is
 * offered at all.
 */
export function describeUndoPreflight(p: UndoConflictReport): { canUndo: boolean; message: string } {
  const conflicts =
    (p.content_changed?.length ?? 0) +
    (p.book_deleted?.length ?? 0) +
    (p.re_organized?.length ?? 0) +
    (p.series_deleted?.length ?? 0) +
    (p.series_renamed_since?.length ?? 0) +
    (p.series_name_taken?.length ?? 0);
  const restorable = (p.safe ?? 0) + conflicts;
  const notRestorable = p.not_restorable ?? 0;
  const types = formatTypeCounts(p.not_restorable_types);
  const recordOnly =
    notRestorable > 0
      ? ` ${notRestorable} change(s) are a record only and cannot be undone automatically${types ? ` (${types})` : ''}.`
      : '';
  if (restorable === 0) {
    const already = p.already_reverted > 0 ? ` ${p.already_reverted} change(s) were already undone.` : '';
    return { canUndo: false, message: `Nothing in this operation can be undone automatically.${recordOnly}${already}` };
  }
  if (conflicts > 0) {
    return {
      canUndo: true,
      message: `${restorable} change(s) can be undone; ${conflicts} of them have conflicts.${recordOnly} Proceed?`,
    };
  }
  return { canUndo: true, message: `Undo ${restorable} change(s) from this operation?${recordOnly}` };
}
