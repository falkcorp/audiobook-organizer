// file: web/src/pages/libraryCombineToast.ts
// version: 1.5.0
// guid: 153032e4-1d3e-45a8-9827-f498afe24850
// last-edited: 2026-10-22

/**
 * Pure wording/decision helpers for the Library "Combine into One Book" toasts.
 * Kept out of Library.tsx so every outcome (success, warnings, pending undo
 * journal, mid-way failure, undo result) is unit-testable without driving the
 * whole page.
 *
 * The combine itself runs as a book-combine operation (POST /audiobooks/combine
 * answers 202); its outcome wording is describeCombineOutcome in
 * utils/combineOutcome.ts. The undo is POST /operations/:id/revert with the
 * combine's operation_id, reached from the toast's action button: the action is
 * offered only when the result carried an id, the text mentions Undo only then,
 * and a partial or failed undo offers Retry (a re-run of the same revert picks
 * up the rows that did not revert).
 *
 * Every toast that leaves the combine half-done (stopped part-way, partly
 * undone, undo failed) also names the operation id: a toast is dismissed or
 * times out, and after that the id is the only handle on the revert.
 */

import { ApiError, type RevertOperationResult } from '../services/api';
import { operationRef, type CombineOutcomePlan } from '../utils/combineOutcome';

/** Toast text and action for one combine or undo outcome; see combineOutcome.ts. */
export type CombineToastPlan = CombineOutcomePlan;

/**
 * The error of POST /audiobooks/combine itself: a refusal made before the
 * combine was queued (400, 404, 409 with the reason in the message). What the
 * combine did once queued is its operation's result, described by
 * describeCombineOutcome.
 */
export function describeCombineFailure(error: unknown): CombineToastPlan {
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
    // The server's message names what was not undone and, for a combine
    // undone with things left as they are, each of those notes. Without it
    // the toast only said that something was left.
    const serverMessage = result.message?.trim();
    const details = serverMessage ? ` ${serverMessage.replace(/[.;:,]?$/, '.')}` : '';
    return {
      message: `Some of the combine could not be undone${counts}.${details} Retry runs the undo again for what is left${operationRef(operationId)}.`,
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
