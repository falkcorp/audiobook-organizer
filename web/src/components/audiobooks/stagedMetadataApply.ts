// file: web/src/components/audiobooks/stagedMetadataApply.ts
// version: 1.0.1
// guid: 2c7a9e14-5b3f-4d81-a6e0-9f1b8d3c7e52
// last-edited: 2026-10-06
//
// The Search Metadata dialog's staged pick and the detached background apply
// that runs it when the dialog closes. Kept out of the component file so the
// dialog module exports only components (react-refresh).

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

/**
 * submitStagedApply sends one staged pick as ONE background apply and follows
 * it to the end. It is deliberately detached from the dialog: the dialog has
 * already closed when this runs, so it touches only the caller-owned toast and
 * onApplied, never dialog state. The reviewer keeps working while it polls.
 */
export async function submitStagedApply(args: {
  book: Book;
  pick: StagedPick;
  writeToFiles: boolean;
  toast: ToastFn;
  onApplied: (updatedBook: Book) => void;
}): Promise<void> {
  const { book, pick, writeToFiles, toast, onApplied } = args;
  const label = book.title ? `"${book.title}"` : 'this book';
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
  let resp: Awaited<ReturnType<typeof api.applyMetadataCandidate>>;
  try {
    resp = await api.applyMetadataCandidate(
      book.id,
      pick.candidate,
      pick.fields,
      writeToFiles,
      pick.overrideAsin,
      { background: true }
    );
  } catch (err) {
    const conflict = api.asinConflictOf(err);
    if (conflict) {
      // The search did not flag it (or the book changed since): the dialog is
      // gone, so the confirmation rides on the toast.
      toast(
        `Not applied to ${label}: the candidate's ASIN ${conflict.candidateAsin || ''} is not the book's (${conflict.bookAsin}).`,
        'warning',
        {
          label: 'Apply anyway',
          onClick: () =>
            void submitStagedApply({ ...args, pick: { ...pick, overrideAsin: conflict.bookAsin } }),
        }
      );
      return;
    }
    toast(err instanceof Error ? err.message : `Failed to apply metadata to ${label}`, 'error');
    return;
  }
  if (!resp.operation_id) {
    // A server with no background op applied it inline.
    onApplied(resp.book);
    toast(`Metadata applied to ${label} from ${resp.source}`, 'success', undoAction);
    return;
  }
  toast(`Applying metadata to ${label} in the background`, 'info');
  let op: api.OperationV2;
  try {
    // A per-request deadline so one hung status read cannot leave this
    // book's outcome unreported forever.
    op = await api.pollOperationV2(resp.operation_id, undefined, 1500, {
      requestTimeoutMs: 15000,
    });
  } catch (err) {
    toast(
      `Lost track of the metadata apply for ${label} (operation ${resp.operation_id}); check Operations. ${
        err instanceof Error ? err.message : ''
      }`.trim(),
      'warning'
    );
    return;
  }
  if (op.status !== 'completed') {
    toast(
      `Metadata apply for ${label} ${op.status}${op.error_message ? `: ${op.error_message}` : ''}`,
      'error'
    );
    return;
  }
  let fresh: Book = resp.book;
  try {
    fresh = await api.getBook(book.id);
  } catch {
    /* the 202's pre-apply book is the fallback; the toast still reports success */
  }
  if (fresh) onApplied(fresh);
  toast(`Metadata applied to ${label} from ${pick.candidate.source}`, 'success', undoAction);
}
