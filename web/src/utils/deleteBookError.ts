// file: web/src/utils/deleteBookError.ts
// version: 1.7.1
// guid: 0050bdd1-b903-4562-8a05-997c48a46fbf
// last-edited: 2026-10-27

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

// The refusal of a book that owns no live file row but holds retired versions
// (409, code HOLDS_RETIRED_VERSIONS, database.ErrBookHoldsRetiredVersions,
// which also wraps ErrBookOwnsFiles). It replaced OWNS_FILES for that case
// (2026-10-27): the rows are kept under this very book as retired versions (for
// another version of the book) and cannot be moved, so the advice for
// OWNS_FILES (move the files) does not apply.
const HOLDS_RETIRED_VERSIONS_CODE = 'HOLDS_RETIRED_VERSIONS';
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

/**
 * Reports whether a delete or purge was refused because the book owns no live
 * file row but holds retired versions: file rows of another book that were
 * retired into it and cannot be moved (409, code HOLDS_RETIRED_VERSIONS).
 */
export function isHoldsRetiredVersionsRefusal(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    error.status === 409 &&
    errorCode(error) === HOLDS_RETIRED_VERSIONS_CODE
  );
}

/**
 * Reports whether a delete or purge was refused because the book still owns
 * file rows: its own (OWNS_FILES) or retired ones it holds
 * (HOLDS_RETIRED_VERSIONS, a refusal of the same family).
 */
export function isOwnsFilesRefusal(error: unknown): boolean {
  if (!(error instanceof ApiError) || error.status !== 409) return false;
  const code = errorCode(error);
  if (code === OWNS_FILES_CODE || code === HOLDS_RETIRED_VERSIONS_CODE) return true;
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
 * Reports whether a delete or purge was refused because the library folder is
 * not mounted or not answering (503, code ROOT_NOT_READY). Nothing was deleted,
 * and it works again when the folder is back.
 */
export function isRootNotReadyRefusal(error: unknown): boolean {
  if (!(error instanceof ApiError) || error.status !== 503) return false;
  const data = error.data as { code?: unknown } | undefined;
  return data?.code === 'ROOT_NOT_READY';
}

/**
 * Returns the message to show when deleting (or purging) a book failed.
 *
 * A STATE_MOVED_BOOK_KEPT or PROGRESS_DISCARDED_BOOK_KEPT error says what
 * happened to the progress and shows the server's message. A
 * HOLDS_RETIRED_VERSIONS refusal says the book has file rows kept for
 * another version of the book (retired versions), which cannot be moved
 * (soft-deleting a live book is offered, as for OWNS_FILES; a book already in the trash gets no such
 * advice). A 409 "still owns file rows" refusal of a live book becomes a plain sentence with the row
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
    if (isHoldsRetiredVersionsRefusal(error)) {
      const m = /\((\d+) retired row\(s\)/.exec(error.message);
      const count = m ? Number(m[1]) : undefined;
      const rows =
        count === undefined ? 'file rows' : `${count} file row${count === 1 ? '' : 's'}`;
      return (
        `This book has ${rows} kept for another version of the book (retired versions). ` +
        "They can't be moved, so the book can't be permanently deleted while they exist." +
        (options.inTrash ? '' : ' Soft-delete it instead (it stays hidden and restorable).')
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
    if (isRootNotReadyRefusal(error)) {
      return (
        "Nothing was deleted: the library folder isn't mounted (or isn't answering), so the " +
        "server can't tell a missing file from a missing drive. Check the drive, then try " +
        `again. (${error.message})`
      );
    }
    if (error.message) {
      return error.message;
    }
  }
  return fallback;
}

/**
 * What to say about the "block this file's hash" part of a delete. Only when a
 * block was asked for: a hash the server did not block says why (its
 * `warnings`, e.g. a live book still holds the file), instead of a bare
 * "could not be blocked". The delete itself succeeded either way.
 */
export function describeBlockOutcome(
  requested: boolean,
  result: { blocked?: boolean; warnings?: string[] }
): { notice: string; severity: 'success' | 'warning' } {
  if (!requested) return { notice: '', severity: 'success' };
  if (result.blocked) return { notice: ' Hash blocked.', severity: 'success' };
  const why = result.warnings && result.warnings.length > 0 ? ` ${result.warnings.join(' ')}` : '';
  return { notice: why || ' Hash could not be blocked.', severity: 'warning' };
}
