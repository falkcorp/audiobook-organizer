// file: web/src/utils/combineOutcome.ts
// version: 1.0.3
// guid: 5b8e2d71-c0a4-4f36-9e18-7a3d6f1b9c42
// last-edited: 2026-10-27

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
  /**
   * The combine was refused only because the library index or the holder list is
   * still loading (right after a restart), or because other writers kept changing
   * the files it moves; no absorbed book's file moved, and the same combine can be
   * queued again shortly. There is no Undo: the survivor's own-file attach may
   * already have run, but a retry finds it done. Never set together with
   * `failed`. retry_reason is the server's own text (it goes in the log, not the
   * toast); retry_cause says which of the two it was.
   */
  retry_later?: boolean;
  retry_reason?: string;
  /**
   * Why a retry_later combine was refused: 'busy' (other writers kept changing
   * the files it moves) or 'loading' (the library index is still loading). A
   * result without it is read as 'loading'.
   */
  retry_cause?: 'busy' | 'loading';
}

export interface CombineOutcomePlan {
  message: string;
  severity: 'success' | 'warning' | 'error' | 'info';
  /** Non-empty when the toast should carry an action reverting this operation. */
  undoOperationId?: string;
  /** Label of that action: 'Undo' on a combine toast, 'Retry' on an undo toast. */
  actionLabel?: string;
}

/**
 * Said when a combine that changed the library carries no undo id. The server
 * always stores one, so this is a server fault or an older server; the toast
 * must not just lose its Undo button without a word.
 */
export const UNDO_ID_UNAVAILABLE =
  "Undo id unavailable: open the combined book's Files & History tab, or the operation, to find what to revert";

/**
 * Said when the combine was refused because the library is still loading. A
 * warning, not an error: nothing is wrong and no file was moved from the other
 * books.
 */
export const COMBINE_RETRY_LATER_LOADING_MESSAGE =
  'The library is still loading; try the combine again shortly. No files were moved from the other books.';

/**
 * Said when the combine was refused because other writers kept changing the
 * files it moves. Same severity and same promise as the loading message.
 */
export const COMBINE_RETRY_LATER_BUSY_MESSAGE =
  'Other writers are changing the files the combine moves; try the combine again shortly. No files were moved from the other books.';

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

  // Refused only because the library is still loading or its files are busy:
  // the operation ends completed with this recorded, so it is read before the
  // status. No Undo, as no absorbed file moved, and not an error.
  if (result?.retry_later && !result.failed) {
    return {
      severity: 'warning',
      message:
        result.retry_cause === 'busy'
          ? COMBINE_RETRY_LATER_BUSY_MESSAGE
          : COMBINE_RETRY_LATER_LOADING_MESSAGE,
    };
  }

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
          : `The combine stopped part-way and may already have moved some files or put some books in the trash. ${UNDO_ID_UNAVAILABLE}${operationRef(operationId)}.`,
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

  // No stored result (or one that does not describe a finished combine): the
  // operation may have moved files or trashed books before it stopped, and the
  // journal names the operation by its own id, so Undo is offered with that id.
  // If nothing was journalled the revert answers 409 and the undo toast says so.
  if (status !== 'completed') {
    const undoOperationId = result?.operation_id || operationId;
    return {
      severity: 'error',
      message: `The combine did not complete (${status})${errorMessage ? `: ${errorMessage}` : ''}. It may already have changed some books; Undo puts back whatever it changed${operationRef(operationId)}.`,
      undoOperationId,
      actionLabel: 'Undo',
    };
  }
  if (!result) {
    return {
      severity: 'warning',
      message: `The combine finished but reported no result, so what it did is unknown. Undo puts back whatever it changed${operationRef(operationId)}.`,
      undoOperationId: operationId,
      actionLabel: 'Undo',
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
  if (!undoOperationId) {
    sentences.push(`${UNDO_ID_UNAVAILABLE}${operationRef(operationId)}.`);
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
