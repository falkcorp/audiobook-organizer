// file: web/src/utils/combineOutcome.ts
// version: 1.0.0
// guid: 5b8e2d71-c0a4-4f36-9e18-7a3d6f1b9c42
// last-edited: 2026-10-22

/** The book-combine operation's result payload (GET operation result). */
export interface BookCombineOpResult {
  primary_id: string;
  files_moved: number;
  books_deleted: number;
  /** Combine journal id (GET /merge/combine-journal lists it). */
  journal_id?: string;
  /**
   * Undo key: POST /operations/:id/revert with it reverses this combine. On
   * success it is the combine operation's own id.
   */
  operation_id?: string;
  /**
   * Set when the combine applied but its undo journal could not be marked
   * applied. It stays pending and lists every step that ran, so reverting
   * operation_id still replays it.
   */
  journal_pending?: string;
  /** What the combine did not do that was asked (an override it could not journal). */
  warnings?: string[];
  message?: string;
  /** the operation failed; the server stores failed, error and, when part-way, incomplete */
  failed?: boolean;
  /**
   * with `failed`: the combine stopped part-way, so some files may have moved
   * or some books gone to the trash. journal_id/operation_id name the undo.
   * Absent or false: it was refused before any write.
   */
  incomplete?: boolean;
  error?: string;
}

export interface CombineOutcomePlan {
  message: string;
  severity: 'success' | 'warning' | 'error' | 'info';
  /** Non-empty when the toast should carry an action reverting this operation. */
  undoOperationId?: string;
  /** Label of that action: 'Undo' on a combine toast, 'Retry' on an undo toast. */
  actionLabel?: string;
}

/** " (operation <id>)", so a half-done combine can still be found and reverted after its toast is gone. */
export function operationRef(operationId: string): string {
  return ` (operation ${operationId})`;
}

/**
 * What to tell the user once the combine operation has ended. Queueing the
 * combine is not combining: the operation can be refused or stop part-way, and
 * neither is ever described as "Combined".
 *
 * `operationId` is the combine operation's own id, used to point at it when
 * the outcome cannot be read.
 */
export function describeCombineOutcome(args: {
  status: string;
  errorMessage?: string | null;
  result: BookCombineOpResult | null;
  operationId: string;
}): CombineOutcomePlan {
  const { status, errorMessage, result, operationId } = args;

  if (result?.failed) {
    if (result.incomplete) {
      // The server's own text names the operation to revert; the Undo button
      // is that revert, so say what it does and keep the id for after the
      // toast is gone.
      const undoOperationId = result.operation_id || undefined;
      return {
        severity: 'error',
        message: undoOperationId
          ? `The combine stopped part-way and may already have moved some files or put some books in the trash. Undo puts back what it changed${operationRef(undoOperationId)}.`
          : `The combine stopped part-way and may already have moved some files or put some books in the trash. Check the operation to see what it did${operationRef(operationId)}.`,
        undoOperationId,
        actionLabel: undoOperationId ? 'Undo' : undefined,
      };
    }
    const reason = result.error || errorMessage || '';
    return {
      severity: 'error',
      message: `The combine was refused before changing anything${reason ? `: ${reason}` : ''}. Nothing was combined.`,
    };
  }

  if (status !== 'completed') {
    // No stored result: the operation may have written before it stopped, so
    // never claim nothing was combined.
    return {
      severity: 'error',
      message: `The combine did not complete (${status})${errorMessage ? `: ${errorMessage}` : ''}. Check the operation to see what it did${operationRef(operationId)}.`,
    };
  }
  if (!result) {
    return {
      severity: 'warning',
      message: `The combine finished but reported no result, so what it did is unknown${operationRef(operationId)}.`,
    };
  }

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
