// file: web/src/utils/deleteBookError.ts
// version: 1.2.0
// guid: 0050bdd1-b903-4562-8a05-997c48a46fbf
// last-edited: 2026-10-06

import { ApiError } from '../services/api';

// The server refuses a permanent delete of a book that still owns file rows
// (409, code OWNS_FILES, database.ErrBookOwnsFiles): deleting the book would
// leave those rows pointing at nothing. Its message reads "... book still owns
// book_file rows (N row(s)); ..." -- the row count is read from it when there.
// The message marker is only the fallback for a server from before the code
// (2026-10-06), which answered the generic CONFLICT.
const OWNS_FILES_CODE = 'OWNS_FILES';
const OWNS_FILES_MARKER = 'still owns book_file rows';

/** Reports whether a delete or purge was refused because the book still owns file rows. */
export function isOwnsFilesRefusal(error: unknown): boolean {
  if (!(error instanceof ApiError) || error.status !== 409) return false;
  const data = error.data as { code?: unknown } | undefined;
  return data?.code === OWNS_FILES_CODE || error.message.includes(OWNS_FILES_MARKER);
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

/**
 * Returns the message to show when deleting (or purging) a book failed.
 *
 * A 409 "still owns file rows" refusal becomes a plain sentence with the row
 * count and what to do next. Any other server error shows the server's own
 * message; anything else shows the caller's fallback.
 */
export function describeDeleteBookError(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    if (isOwnsFilesRefusal(error)) {
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
