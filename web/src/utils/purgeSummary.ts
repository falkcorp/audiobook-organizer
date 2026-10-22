// file: web/src/utils/purgeSummary.ts
// version: 1.0.0
// guid: 7c3a9e15-2d84-4b60-a1f7-5e0d8b4c6a29
// last-edited: 2026-10-22

import type { PurgeBreakdown, PurgeResult } from '../services/api';

/** One reason a trashed book is kept by the purge, with how many. */
export interface KeptReason {
  key: 'owns_files' | 'undo_possible' | 'has_progress';
  count: number;
  /** Plain-language reason, starting with a lowercase verb phrase. */
  label: string;
}

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

/**
 * The reasons the purge keeps books, in the order the dialog lists them,
 * omitting any reason with a zero count. A book is counted under exactly one
 * reason: owning book_file rows first, then a combine that can still be undone,
 * then listening progress.
 */
export function keptReasons(counts: {
  ownsFiles: number;
  undoPossible: number;
  hasProgress: number;
}): KeptReason[] {
  const out: KeptReason[] = [];
  if (counts.ownsFiles > 0) {
    out.push({
      key: 'owns_files',
      count: counts.ownsFiles,
      label: `still ${plural(counts.ownsFiles, 'owns', 'own')} file records (the purge never deletes those)`,
    });
  }
  if (counts.undoPossible > 0) {
    out.push({
      key: 'undo_possible',
      count: counts.undoPossible,
      label: `${plural(counts.undoPossible, 'is the', 'are the')} leftover ${plural(
        counts.undoPossible,
        'copy',
        'copies'
      )} of a combine that can still be undone (purging would make the undo impossible)`,
    });
  }
  if (counts.hasProgress > 0) {
    out.push({
      key: 'has_progress',
      count: counts.hasProgress,
      label: `${plural(counts.hasProgress, 'has', 'have')} listening progress and no other copy to move it to`,
    });
  }
  return out;
}

export function keptReasonsOfResult(r: PurgeResult): KeptReason[] {
  return keptReasons({
    ownsFiles: r.skipped_owns_files ?? 0,
    undoPossible: r.kept_undo_possible ?? 0,
    hasProgress: r.kept_has_progress ?? 0,
  });
}

export function keptReasonsOfBreakdown(b: PurgeBreakdown): KeptReason[] {
  return keptReasons({
    ownsFiles: b.owns_files,
    undoPossible: b.undo_possible,
    hasProgress: b.has_listening_state,
  });
}

const books = (n: number) => `${n.toLocaleString()} ${plural(n, 'book', 'books')}`;

/** The one-line headline of a preview: what a purge would do right now. */
export function previewHeadline(r: PurgeResult): string {
  const purgeable = r.purgeable ?? 0;
  if (r.attempted === 0) return 'There are no soft-deleted books to purge.';
  if (purgeable === 0) {
    return `A purge would remove none of the ${books(r.attempted)} in the trash right now.`;
  }
  return `A purge would remove ${books(purgeable)} of the ${r.attempted.toLocaleString()} in the trash right now.`;
}

/**
 * The headline of a finished purge. Says how many were removed out of how many,
 * so a purge that removed none reads as "none", never as success.
 */
export function outcomeHeadline(r: PurgeResult): string {
  if (r.attempted === 0) return 'There were no soft-deleted books to purge.';
  if (r.purged === 0) return `Purged none of the ${books(r.attempted)} in the trash.`;
  const files =
    r.files_deleted > 0
      ? `, and deleted ${r.files_deleted.toLocaleString()} ${plural(r.files_deleted, 'file', 'files')} from disk`
      : '';
  return `Purged ${r.purged.toLocaleString()} of ${r.attempted.toLocaleString()} soft-deleted books${files}.`;
}

/** Severity the finished purge should be shown with. */
export function outcomeSeverity(r: PurgeResult): 'success' | 'warning' | 'error' {
  if ((r.failed ?? 0) > 0 || (r.carry_failed ?? 0) > 0 || r.errors.length > 0) return 'warning';
  return 'success';
}

/** Extra lines about listening progress carried over and what went wrong. */
export function outcomeNotes(r: PurgeResult): string[] {
  const notes: string[] = [];
  const carried = r.carried_to_sibling ?? 0;
  if (carried > 0) {
    notes.push(
      `${books(carried)} had listening progress, which was moved to the copy in the library before the book was purged.`
    );
  }
  const carryFailed = r.carry_failed ?? 0;
  if (carryFailed > 0) {
    notes.push(
      `Moving the listening progress failed for ${books(carryFailed)}; ${plural(carryFailed, 'it was', 'they were')} kept with the progress.`
    );
  }
  const failed = r.failed ?? 0;
  if (failed > 0) {
    notes.push(`${books(failed)} could not be processed (see the details below).`);
  }
  return notes;
}
