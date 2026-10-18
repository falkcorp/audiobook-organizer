// file: web/src/pages/libraryCombineToast.ts
// version: 1.3.0
// guid: 153032e4-1d3e-45a8-9827-f498afe24850
// last-edited: 2026-10-18

/**
 * Pure wording/decision helpers for the Library "Combine into One Book" toasts.
 * Kept out of Library.tsx so every outcome (success, warnings, pending undo
 * journal, mid-way failure, undo result) is unit-testable without driving the
 * whole page.
 *
 * The undo is POST /operations/:id/revert with the combine's operation_id. A
 * manual combine runs in no operation of its own (the id is a correlation id
 * with no operations row), so the toast's action button is the only place in
 * the UI that reaches it: the action is offered only when the server returned
 * an id, the text mentions Undo only then, and a partial or failed undo offers
 * Retry (a re-run of the same revert picks up the rows that did not revert)
 * rather than pointing at a page that may not list the operation.
 *
 * Every toast that leaves the combine half-done (stopped part-way, partly
 * undone, undo failed) also names the operation id: a toast is dismissed or
 * times out, and after that the id is the only handle on the revert.
 */

import { ApiError, type CombineBooksResult, type RevertOperationResult } from '../services/api';

export type CombineToastSeverity = 'success' | 'warning' | 'error';

export interface CombineToastPlan {
  message: string;
  severity: CombineToastSeverity;
  /** Non-empty when the toast should carry an action reverting this operation. */
  undoOperationId?: string;
  /** Label of that action: 'Undo' on a combine toast, 'Retry' on an undo toast. */
  actionLabel?: string;
}

/** " (operation <id>)", so a half-done combine can still be found and reverted after its toast is gone. */
function operationRef(operationId: string): string {
  return ` (operation ${operationId})`;
}

export function describeCombineResult(result: CombineBooksResult): CombineToastPlan {
  const combined = `Combined ${result.files_moved} files into one book; moved ${result.books_deleted} entries to trash.`;
  const undoOperationId = result.operation_id ? result.operation_id : undefined;
  const sentences: string[] = [];
  if (result.journal_pending) {
    sentences.push(
      undoOperationId
        ? 'Its undo record could not be finalized, but it lists every step, so Undo still puts them back.'
        : 'Its undo record could not be finalized.'
    );
  }
  if (result.warnings?.length) {
    sentences.push(result.warnings.join(' '));
  }
  return {
    message: [combined, ...sentences].join(' '),
    severity: sentences.length > 0 ? 'warning' : 'success',
    undoOperationId,
    actionLabel: undoOperationId ? 'Undo' : undefined,
  };
}

/** Operation id carried by a COMBINE_INCOMPLETE error body, if any. */
export function combineIncompleteOperationId(error: unknown): string | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const data = error.data as { operation_id?: unknown } | null | undefined;
  const id = data?.operation_id;
  return typeof id === 'string' && id !== '' ? id : undefined;
}

export function describeCombineFailure(error: unknown): CombineToastPlan {
  const undoOperationId = combineIncompleteOperationId(error);
  if (undoOperationId) {
    // The server's message names the operation id to revert; the button is
    // that revert, so the toast says what Undo does instead of repeating it,
    // and keeps the id for after the toast is gone.
    return {
      message: `The combine stopped part-way and may already have moved some files or put some books in the trash. Undo puts back what it changed${operationRef(undoOperationId)}.`,
      severity: 'error',
      undoOperationId,
      actionLabel: 'Undo',
    };
  }
  const base =
    error instanceof ApiError && error.message ? error.message : 'Failed to combine books.';
  return { message: base, severity: 'error' };
}

export function describeUndoResult(
  result: RevertOperationResult,
  operationId: string
): CombineToastPlan {
  if (result.partial) {
    const counts =
      typeof result.restored === 'number' && typeof result.total === 'number'
        ? ` (${result.restored} of ${result.total} changes reverted)`
        : '';
    return {
      message: `Some of the combine could not be undone${counts}. Retry runs the undo again for what is left${operationRef(operationId)}.`,
      severity: 'warning',
      undoOperationId: operationId,
      actionLabel: 'Retry',
    };
  }
  if ((result.combines_undone_in_part ?? 0) > 0) {
    // Undone, but the undo left something as it is (a credit or lock added
    // since, an author it created that no book credits now). The server's
    // message lists each one; a warning, and no Retry: running it again
    // would leave the same things.
    return {
      message: result.message || 'Combine undone; some things were left as they are.',
      severity: 'warning',
    };
  }
  return { message: result.message || 'Combine undone.', severity: 'success' };
}

export function describeUndoFailure(error: unknown, operationId: string): CombineToastPlan {
  const reason = error instanceof ApiError && error.message ? ` ${error.message}` : '';
  // 409: the server refused (nothing restorable, or the library changed since
  // the combine); running it again would refuse the same way.
  if (error instanceof ApiError && error.status === 409) {
    return { message: `The combine was not undone.${reason}`, severity: 'error' };
  }
  return {
    message: `Failed to undo the combine${operationRef(operationId)}.${reason}`,
    severity: 'error',
    undoOperationId: operationId,
    actionLabel: 'Retry',
  };
}
