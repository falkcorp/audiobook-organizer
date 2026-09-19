// file: web/src/utils/deleteBookError.ts
// version: 1.0.0
// guid: 0050bdd1-b903-4562-8a05-997c48a46fbf
// last-edited: 2026-09-19

import { ApiError } from '../services/api';

// The server refuses a permanent delete of a book that still owns file rows
// (409, database.ErrBookOwnsFiles): deleting the book would leave those rows
// pointing at nothing. Its message reads "... book still owns book_file rows
// (N row(s)); ...".
const OWNS_FILES_MARKER = 'still owns book_file rows';

/**
 * Returns the message to show when deleting (or purging) a book failed.
 *
 * A 409 "still owns file rows" refusal becomes a plain sentence with the row
 * count and what to do next. Any other server error shows the server's own
 * message; anything else shows the caller's fallback.
 */
export function describeDeleteBookError(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    if (error.status === 409 && error.message.includes(OWNS_FILES_MARKER)) {
      const m = /\((\d+) row\(s\)\)/.exec(error.message);
      const count = m ? Number(m[1]) : undefined;
      const rows =
        count === undefined ? 'file rows' : `${count} file row${count === 1 ? '' : 's'}`;
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
