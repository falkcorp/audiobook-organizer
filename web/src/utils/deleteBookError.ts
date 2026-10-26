// file: web/src/utils/deleteBookError.ts
// version: 1.5.0
// guid: 0050bdd1-b903-4562-8a05-997c48a46fbf
// last-edited: 2026-10-26

import { ApiError } from '../services/api';

// The server refuses a permanent delete of a book that still owns file rows
// (409, code OWNS_FILES, database.ErrBookOwnsFiles): deleting the book would
// leave those rows pointing at nothing. Its message reads "... book still owns
// book_file rows (N row(s)); ..." -- the row count is read from it when there.
// The message marker is only the fallback for a response with no specific code
// (a server from before the OWNS_FILES code, 2026-10-06, answered the generic
// CONFLICT): a response that carries any other specific code is never
// re-classified by its text.
const OWNS_FILES_CODE = 'OWNS_FILES';
const OWNS_FILES_MARKER = 'still owns book_file rows';
const GENERIC_CODE = 'CONFLICT';

// A delete that moved every user's listening progress to another copy and then
// failed (code STATE_MOVED_BOOK_KEPT: 409 when the cause was file rows, 500
// otherwise). Its message wraps the owns-files text, so it must be recognised
// by its code before the owns-files check.
const STATE_MOVED_CODE = 'STATE_MOVED_BOOK_KEPT';

// A discard-progress-and-purge that erased every user's listening progress and
// whose delete then failed (code PROGRESS_DISCARDED_BOOK_KEPT: 409 when the
// cause was file rows, 500 otherwise). Its message wraps the owns-files text,
// so, like STATE_MOVED_BOOK_KEPT, it is recognised by its code first.
const PROGRESS_DISCARDED_CODE = 'PROGRESS_DISCARDED_BOOK_KEPT';

// A message that names retired rows (kept for another version of the book,
// which cannot be moved) carries wording the server chose for that case.
const RETIRED_ROWS_MARKER = 'retired row';

function errorCode(error: ApiError): string | undefined {
  const data = error.data as { code?: unknown } | undefined;
  return typeof data?.code === 'string' && data.code !== '' ? data.code : undefined;
}

/**
 * Reports whether a delete was refused after every user's listening progress
 * had already been moved to another copy of the book, so the book stayed but
 * its progress did not (code STATE_MOVED_BOOK_KEPT, any status).
 */
export function isStateMovedBookKeptRefusal(error: unknown): boolean {
  return error instanceof ApiError && errorCode(error) === STATE_MOVED_CODE;
}

/**
 * Reports whether a discard-progress-and-purge erased every user's listening
 * progress and was then refused, so the book stayed without it (code
 * PROGRESS_DISCARDED_BOOK_KEPT, any status).
 */
export function isProgressDiscardedBookKeptRefusal(error: unknown): boolean {
  return error instanceof ApiError && errorCode(error) === PROGRESS_DISCARDED_CODE;
}

/** Reports whether a delete or purge was refused because the book still owns file rows. */
export function isOwnsFilesRefusal(error: unknown): boolean {
  if (!(error instanceof ApiError) || error.status !== 409) return false;
  const code = errorCode(error);
  if (code === OWNS_FILES_CODE) return true;
  if (code !== undefined && code !== GENERIC_CODE) return false;
  return error.message.includes(OWNS_FILES_MARKER);
}

/**
 * Reports whether a delete or purge was refused because users have listening
 * progress on the book and there is no copy in the Audiobookshelf library to
 * move it to (409, code HAS_PROGRESS). The book and its progress were left
 * as they were; for a book in the trash the owner can choose "Discard
 * progress and purge".
 */
export function isHasProgressRefusal(error: unknown): boolean {
  if (!(error instanceof ApiError) || error.status !== 409) return false;
  const data = error.data as { code?: unknown } | undefined;
  return data?.code === 'HAS_PROGRESS';
}

export interface DescribeDeleteBookErrorOptions {
  /**
   * The book is already in the trash (a purge, or discard progress and purge).
   * The server's wording for an owns-files refusal is then shown as it is: it
   * names what to do for a trashed book, and the canned sentence's advice to
   * soft-delete the book would be impossible to follow.
   */
  inTrash?: boolean;
}

/**
 * Returns the message to show when deleting (or purging) a book failed.
 *
 * A STATE_MOVED_BOOK_KEPT or PROGRESS_DISCARDED_BOOK_KEPT error says what
 * happened to the progress and shows the server's message. A 409 "still owns
 * file rows" refusal of a live book becomes a plain sentence with the row
 * count and what to do next, except when the rows are retired ones, which
 * cannot be moved, or the book is already in the trash (options.inTrash): the
 * server's own wording is shown then. Any other server error shows the
 * server's own message; anything else shows the caller's fallback.
 */
export function describeDeleteBookError(
  error: unknown,
  fallback: string,
  options: DescribeDeleteBookErrorOptions = {}
): string {
  if (error instanceof ApiError) {
    if (isStateMovedBookKeptRefusal(error)) {
      return (
        "The book was not deleted, but every user's listening progress on it had already been " +
        `moved to another copy of the book. ${error.message}`
      );
    }
    if (isProgressDiscardedBookKeptRefusal(error)) {
      return (
        'The book was not purged, but its stored listening state (progress, bookmarks or ' +
        `reset markers) had already been erased. ${error.message}`
      );
    }
    if (
      isOwnsFilesRefusal(error) &&
      !options.inTrash &&
      !error.message.includes(RETIRED_ROWS_MARKER)
    ) {
      const m = /\((\d+) row\(s\)\)/.exec(error.message);
      const count = m ? Number(m[1]) : undefined;
      const rows = count === undefined ? 'file rows' : `${count} file row${count === 1 ? '' : 's'}`;
      return (
        `This book still owns ${rows}, so it can't be permanently deleted: that would leave ` +
        'those rows pointing at nothing. Soft-delete it instead (it stays hidden and ' +
        'restorable), or move its files to another book first.'
      );
    }
    if (error.message) {
      return error.message;
    }
  }
  return fallback;
}
