// file: web/src/components/review/spine/bookInfo.ts
// version: 1.0.0
// guid: 17b02240-0976-461f-8bd7-30b77b14dd0a
// last-edited: 2026-10-07
//
// Text helpers for the review cards' book-info block (BookInfoPanel), kept
// out of the component file so it exports components only (fast refresh).

import type { CandidateBookInfo } from '../../../services/api';
import { formatDuration, formatFileSize } from './rowState';

/**
 * The book's runtime as the server computed it from its files. A partial
 * runtime (some chapters never probed) is shown as a lower bound with its
 * coverage, never as the book's length: showing it bare is how a 10 h book
 * read as "40m" next to a 10 h candidate. The unreviewable bucket's rows carry
 * only the stored Book.Duration, labelled as such.
 */
export function bookRuntimeLabel(book: CandidateBookInfo): string | undefined {
  if (book.duration_seconds) return formatDuration(book.duration_seconds);
  if (book.runtime_status === 'partial' && book.runtime_lower_bound_seconds) {
    return `at least ${formatDuration(book.runtime_lower_bound_seconds)} (${book.runtime_files_known} of ${book.runtime_files_counted} files measured)`;
  }
  if (book.stored_duration_seconds) {
    return `${formatDuration(book.stored_duration_seconds)} (stored)`;
  }
  return undefined;
}

/** File count as known: the listing's count, else the runtime's file tally. */
export function bookFileCount(book: CandidateBookInfo): number | undefined {
  if (book.file_count) return book.file_count;
  if (book.runtime_files_counted) return book.runtime_files_counted;
  return undefined;
}

/** One line for a dense row: author · narrator · series · format · runtime · size · files. */
export function bookSummaryLine(book: CandidateBookInfo): string {
  const files = bookFileCount(book);
  return [
    book.author,
    book.narrator ? `narr. ${book.narrator}` : '',
    book.series ? `${book.series}${book.series_position ? ` #${book.series_position}` : ''}` : '',
    book.format,
    bookRuntimeLabel(book),
    book.file_size_bytes ? formatFileSize(book.file_size_bytes) : '',
    files ? `${files} file${files === 1 ? '' : 's'}` : '',
  ]
    .filter(Boolean)
    .join(' · ');
}

