// file: web/src/components/audiobooks/stagedMetadataApply.ts
// version: 1.2.0
// guid: 2c7a9e14-5b3f-4d81-a6e0-9f1b8d3c7e52
// last-edited: 2026-10-07
//
// The Search Metadata dialogs' staged picks and the detached background
// applies that run them when a dialog closes: one pick (MetadataSearchDialog)
// or one per book (BulkMetadataSearchDialog). Kept out of the component files
// so the dialog modules export only components (react-refresh).

import type { Book, MetadataCandidate } from '../../services/api';
import * as api from '../../services/api';
import {
  METADATA_APPLY_FIELDS,
  candidateApplyFieldValue,
  type MetadataApplyField,
} from '../../config/metadataApplyFields';

export type ToastFn = (
  message: string,
  severity?: 'success' | 'error' | 'warning' | 'info',
  action?: { label: string; onClick: () => void }
) => void;

/**
 * StagedPick is the one change the dialog will apply when it closes: a
 * candidate, optionally narrowed to some of its fields, plus the book ASIN the
 * reviewer confirmed applying over when the candidate's ASIN conflicts.
 *
 * One per book: the apply endpoint carries one candidate, and two queued
 * applies of the same book would refuse each other (the second sees the
 * first's history rows as an edit made after it was queued). A new pick
 * replaces the staged one.
 */
export interface StagedPick {
  candidate: MetadataCandidate;
  /** undefined = every field the candidate carries. */
  fields?: string[];
  overrideAsin?: string;
}

/** Fields of candidate that an apply-all would write (the ones it has). */
export function candidateFields(candidate: MetadataCandidate): MetadataApplyField[] {
  return METADATA_APPLY_FIELDS.filter((f) => candidateApplyFieldValue(candidate, f) !== undefined);
}

export function stagedFieldCount(pick: StagedPick): number {
  return pick.fields ? pick.fields.length : candidateFields(pick.candidate).length;
}

/** The identity a staged-pick apply needs of its book. */
export interface StagedBookRef {
  id: string;
  title?: string;
}

/**
 * sameCandidate tells whether two candidate objects describe the same search
 * result. Object identity is not enough: re-running a search (going back to a
 * book in the bulk wizard) returns new objects for the same results.
 */
export function sameCandidate(a: MetadataCandidate, b: MetadataCandidate): boolean {
  if (a === b) return true;
  return (
    a.source === b.source &&
    (a.asin ?? '') === (b.asin ?? '') &&
    (a.isbn ?? '') === (b.isbn ?? '') &&
    a.title === b.title &&
    (a.author ?? '') === (b.author ?? '')
  );
}

function bookLabel(book: StagedBookRef): string {
  return book.title ? `"${book.title}"` : 'this book';
}

/** What sending one staged pick as a background apply produced. */
export type StagedApplyStart =
  | { kind: 'started'; operationId: string; book: Book }
  | { kind: 'inline'; book: Book; source: string }
  | { kind: 'conflict'; bookAsin: string; candidateAsin: string }
  | { kind: 'failed'; message: string };

/** How a started background apply ended. */
export type StagedApplyEnd =
  | { kind: 'applied'; book: Book }
  | { kind: 'failed'; message: string }
  | { kind: 'lost'; message: string };

/**
 * startStagedApply sends one staged pick as ONE background apply
 * (`background: true`): the server answers 202 with the operation that runs
 * it. Never throws.
 */
export async function startStagedApply(
  book: StagedBookRef,
  pick: StagedPick,
  writeToFiles: boolean
): Promise<StagedApplyStart> {
  try {
    const resp = await api.applyMetadataCandidate(
      book.id,
      pick.candidate,
      pick.fields,
      writeToFiles,
      pick.overrideAsin,
      { background: true }
    );
    if (!resp.operation_id) {
      // A server with no background op applied it inline.
      return { kind: 'inline', book: resp.book, source: resp.source };
    }
    return { kind: 'started', operationId: resp.operation_id, book: resp.book };
  } catch (err) {
    const conflict = api.asinConflictOf(err);
    if (conflict) {
      return {
        kind: 'conflict',
        bookAsin: conflict.bookAsin,
        candidateAsin: conflict.candidateAsin || '',
      };
    }
    return {
      kind: 'failed',
      message:
        err instanceof Error ? err.message : `Failed to apply metadata to ${bookLabel(book)}`,
    };
  }
}

/**
 * awaitStagedApply follows a started background apply to its end and reads
 * the book back. Never throws.
 */
export async function awaitStagedApply(
  book: StagedBookRef,
  started: { operationId: string; book: Book }
): Promise<StagedApplyEnd> {
  const label = bookLabel(book);
  let op: api.OperationV2;
  try {
    // A per-request deadline so one hung status read cannot leave this
    // book's outcome unreported forever.
    op = await api.pollOperationV2(started.operationId, undefined, 1500, {
      requestTimeoutMs: 15000,
    });
  } catch (err) {
    return {
      kind: 'lost',
      message:
        `Lost track of the metadata apply for ${label} (operation ${started.operationId}); check Operations. ${
          err instanceof Error ? err.message : ''
        }`.trim(),
    };
  }
  if (op.status !== 'completed') {
    return {
      kind: 'failed',
      message: `Metadata apply for ${label} ${op.status}${op.error_message ? `: ${op.error_message}` : ''}`,
    };
  }
  let fresh: Book = started.book;
  try {
    fresh = await api.getBook(book.id);
  } catch {
    /* the 202's pre-apply book is the fallback; the outcome is still success */
  }
  return { kind: 'applied', book: fresh };
}

/**
 * submitStagedApply sends one staged pick as ONE background apply and follows
 * it to the end. It is deliberately detached from the dialog: the dialog has
 * already closed when this runs, so it touches only the caller-owned toast and
 * onApplied, never dialog state. The reviewer keeps working while it polls.
 *
 * Resolves true only when the apply landed (inline, or the background op
 * finished); false for a conflict, a failure, or a lost op. An "Apply anyway"
 * taken from the conflict toast is a later, separate submit.
 */
export async function submitStagedApply(args: {
  book: Book;
  pick: StagedPick;
  writeToFiles: boolean;
  toast: ToastFn;
  onApplied: (updatedBook: Book) => void;
}): Promise<boolean> {
  const { book, pick, writeToFiles, toast, onApplied } = args;
  const label = bookLabel(book);
  const undoAction = {
    label: 'Undo',
    onClick: async () => {
      try {
        await api.undoLastApply(book.id);
        toast('Metadata apply undone', 'info');
      } catch (err) {
        toast(err instanceof Error ? err.message : 'Undo failed', 'error');
      }
    },
  };
  const start = await startStagedApply(book, pick, writeToFiles);
  switch (start.kind) {
    case 'conflict':
      // The search did not flag it (or the book changed since): the dialog is
      // gone, so the confirmation rides on the toast.
      toast(
        `Not applied to ${label}: the candidate's ASIN ${start.candidateAsin} is not the book's (${start.bookAsin}).`,
        'warning',
        {
          label: 'Apply anyway',
          onClick: () =>
            void submitStagedApply({ ...args, pick: { ...pick, overrideAsin: start.bookAsin } }),
        }
      );
      return false;
    case 'failed':
      toast(start.message, 'error');
      return false;
    case 'inline':
      onApplied(start.book);
      toast(`Metadata applied to ${label} from ${start.source}`, 'success', undoAction);
      return true;
  }
  toast(`Applying metadata to ${label} in the background`, 'info');
  const end = await awaitStagedApply(book, start);
  if (end.kind === 'lost') {
    toast(end.message, 'warning');
    return false;
  }
  if (end.kind === 'failed') {
    toast(end.message, 'error');
    return false;
  }
  if (end.book) onApplied(end.book);
  toast(`Metadata applied to ${label} from ${pick.candidate.source}`, 'success', undoAction);
  return true;
}

/** How many applies (and op polls, and undos) a bulk submit runs at once. */
export const STAGED_APPLY_CONCURRENCY = 4;

/**
 * runBounded calls fn for every item with at most `limit` calls in flight.
 * fn must not throw (each step here reports through its result instead).
 */
export async function runBounded<T>(
  items: readonly T[],
  limit: number,
  fn: (item: T) => Promise<void>
): Promise<void> {
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const item = items[next];
      next += 1;
      await fn(item);
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
}

function nameList(books: StagedBookRef[], max = 3): string {
  const names = books.slice(0, max).map(bookLabel);
  const more = books.length > max ? ` and ${books.length - max} more` : '';
  return names.join(', ') + more;
}

/** One book's staged pick, as the bulk dialog hands it over on close. */
export interface StagedBookPick {
  book: StagedBookRef;
  pick: StagedPick;
}

/**
 * submitStagedApplies runs every staged pick of a bulk session as its own
 * background apply (no endpoint applies reviewer-chosen candidates to many
 * books in one request), with at most STAGED_APPLY_CONCURRENCY requests in
 * flight: first every POST (each answers 202 at once), then every operation
 * followed to its end. Detached from the dialog like submitStagedApply; it
 * reports through ONE start toast and one summary toast (plus one warning for
 * failures and one for ASIN conflicts), never one per book. onDone runs once,
 * after every book settled, when at least one changed.
 */
export async function submitStagedApplies(args: {
  entries: StagedBookPick[];
  writeToFiles: boolean;
  toast: ToastFn;
  onDone: () => void;
}): Promise<void> {
  const { entries, writeToFiles, toast, onDone } = args;
  if (entries.length === 0) return;
  const total = entries.length;
  const noun = (n: number) => `${n} book${n === 1 ? '' : 's'}`;
  toast(`Applying metadata to ${noun(total)} in the background`, 'info');

  const applied: StagedBookRef[] = [];
  const failed: { book: StagedBookRef; message: string }[] = [];
  const conflicts: { entry: StagedBookPick; bookAsin: string }[] = [];
  const started: { book: StagedBookRef; operationId: string; startBook: Book }[] = [];

  await runBounded(entries, STAGED_APPLY_CONCURRENCY, async (entry) => {
    const start = await startStagedApply(entry.book, entry.pick, writeToFiles);
    switch (start.kind) {
      case 'started':
        started.push({ book: entry.book, operationId: start.operationId, startBook: start.book });
        return;
      case 'inline':
        applied.push(entry.book);
        return;
      case 'conflict':
        conflicts.push({ entry, bookAsin: start.bookAsin });
        return;
      case 'failed':
        failed.push({ book: entry.book, message: start.message });
    }
  });

  await runBounded(started, STAGED_APPLY_CONCURRENCY, async (s) => {
    const end = await awaitStagedApply(s.book, {
      operationId: s.operationId,
      book: s.startBook,
    });
    if (end.kind === 'applied') applied.push(s.book);
    else failed.push({ book: s.book, message: end.message });
  });

  if (applied.length > 0) {
    const undone = [...applied];
    toast(`Metadata applied to ${applied.length} of ${noun(total)}`, 'success', {
      label: applied.length === 1 ? 'Undo' : `Undo all (${applied.length})`,
      onClick: async () => {
        let ok = 0;
        const errors: string[] = [];
        await runBounded(undone, STAGED_APPLY_CONCURRENCY, async (b) => {
          try {
            await api.undoLastApply(b.id);
            ok += 1;
          } catch (err) {
            errors.push(`${bookLabel(b)}: ${err instanceof Error ? err.message : 'undo failed'}`);
          }
        });
        if (errors.length === 0) toast(`Undid metadata apply for ${noun(ok)}`, 'info');
        else toast(`Undid ${ok} of ${undone.length}; failed: ${errors.join('; ')}`, 'error');
        if (ok > 0) onDone();
      },
    });
  }
  if (failed.length > 0) {
    toast(
      `Metadata apply failed for ${noun(failed.length)}: ${failed
        .slice(0, 3)
        .map((f) => f.message)
        .join('; ')}${failed.length > 3 ? ` (and ${failed.length - 3} more)` : ''}`,
      'error'
    );
  }
  if (conflicts.length > 0) {
    const books = conflicts.map((c) => c.entry.book);
    toast(`Not applied to ${nameList(books)}: the candidate's ASIN is not the book's.`, 'warning', {
      label: `Apply anyway (${conflicts.length})`,
      onClick: () =>
        void submitStagedApplies({
          ...args,
          entries: conflicts.map((c) => ({
            book: c.entry.book,
            pick: { ...c.entry.pick, overrideAsin: c.bookAsin },
          })),
        }),
    });
  }
  if (applied.length > 0) onDone();
}
