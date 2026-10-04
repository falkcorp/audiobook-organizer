// file: web/src/pages/organizeRollback.ts
// version: 1.0.0
// guid: 62e85661-aa7e-4e16-af81-c50ccf37d970
// last-edited: 2026-10-04

import * as api from '../services/api';
import type { Audiobook } from '../types';

/** The fields an organize rollback restores on each book. */
type RollbackUpdate = Pick<Audiobook, 'library_state' | 'file_path' | 'organized_file_hash'>;

/** What the rollback did, and the toast that says so. */
export interface OrganizeRollbackResult {
  total: number;
  restored: number;
  /** The book the rollback stopped at, when an update threw. */
  failed?: { label: string; error: string };
  message: string;
  severity: 'success' | 'warning' | 'error';
}

/**
 * Library's organize rollback: restores each snapshotted book in order and
 * stops at the first update that throws. Books restored before the failure
 * stay restored, so the result names how many were, which book it stopped at
 * and why, plus the partial-save warnings of every book restored so far.
 * Never throws: the caller always clears its cache and reloads, because some
 * books may have changed either way.
 */
export async function runOrganizeRollback(
  books: Audiobook[],
  update: (id: string, u: RollbackUpdate) => Promise<api.UpdateBookResult> = (id, u) =>
    api.updateBook(id, u)
): Promise<OrganizeRollbackResult> {
  const total = books.length;
  const perBook: Array<{ label: string; warnings: string[] }> = [];
  let failed: OrganizeRollbackResult['failed'];
  for (const book of books) {
    const label = book.title || book.id;
    try {
      const { warnings } = api.splitUpdateWarnings(
        await update(book.id, {
          library_state: book.library_state,
          file_path: book.file_path,
          organized_file_hash: book.organized_file_hash,
        })
      );
      perBook.push({ label, warnings });
    } catch (error) {
      failed = { label, error: error instanceof Error ? error.message : String(error) };
      break;
    }
  }
  const restored = perBook.length;
  const warned = api.summarizeUpdateWarnings(perBook);
  const warnText = warned ? `; ${warned.count} book(s) not saved completely: ${warned.text}` : '';
  if (failed) {
    return {
      total,
      restored,
      failed,
      message: `Rolled back ${restored} of ${total}; failed at ${failed.label}: ${failed.error}${warnText}`,
      severity: 'error',
    };
  }
  if (warned) {
    return {
      total,
      restored,
      message: `Rollback complete, but ${warned.count} book(s) not saved completely: ${warned.text}`,
      severity: 'warning',
    };
  }
  return { total, restored, message: 'Rollback complete.', severity: 'success' };
}
