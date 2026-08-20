// file: web/src/components/review/lanes/keepDecision.ts
// version: 1.0.0
// guid: 8f2a6d10-4b73-4c58-9e14-7a0c3b5d29e6
// last-edited: 2026-08-20
//
// Which side of a duplicate pair should survive a merge.
//
// This lived inside UnifiedDedupTab as a private function whose whole purpose
// was keeping two call sites honest: the recommended-keep chip in the render
// and the `m` keyboard shortcut. Its comment promised they "can never drift".
//
// The port splits those two call sites across a hook and a component, so the
// promise no longer holds by proximity and has to hold by construction. Hence a
// module of its own, imported by both. Re-deriving the decision in either place
// would reintroduce exactly the drift the original was written to prevent: a
// reviewer pressing `m` on the row whose ★ chip points at B, and keeping A.

import type { Book, DedupCandidate } from '../../../services/api';

/**
 * A rough completeness score for a book's metadata, used only to compare the
 * two sides of one pair. The absolute value is not meaningful and is not shown
 * to the reviewer -- only the comparison and the band it falls in are.
 *
 * Note this is not a field count. A title is worth nothing if it is a
 * placeholder, and the two placeholders actually present in this library --
 * the literal string "TITLE" and a bare ULID left over from an import that
 * never resolved -- score zero rather than two. Counting them would recommend
 * keeping the side with no real title at all.
 */
export function metadataQuality(book: Book | null | undefined): number {
  if (!book) return 0;
  let score = 0;
  const title = book.title ?? '';
  const isGarbageTitle =
    !title || title.toUpperCase() === 'TITLE' || /^[0-9A-Z]{26}$/.test(title.trim());
  if (!isGarbageTitle) score += 2;
  // An external id is the strongest signal: it is what makes the row
  // re-matchable later, so it outweighs a title.
  if (book.asin) score += 3;
  if (book.isbn13 || book.isbn) score += 2;
  if (book.cover_url) score += 1;
  if (book.narrator) score += 0.5;
  if (book.description) score += 0.5;
  if (book.publisher) score += 0.5;
  return score;
}

/** Quality bands for the chip. Thresholds are shared so the chip and any future caller agree. */
export type QualityBand = 'rich' | 'partial' | 'poor';

export function qualityBand(score: number): QualityBand {
  if (score >= 6) return 'rich';
  if (score >= 3) return 'partial';
  return 'poor';
}

export interface KeepRecommendation {
  keepId: string;
  label: 'A' | 'B';
}

/**
 * Which side to keep, or `null` when the two sides score equally.
 *
 * `null` is a real answer, not a failure: on a tie there is no evidence-based
 * reason to prefer either book, and inventing one would present a coin flip as
 * a recommendation. Callers decide what to do with a tie -- the keyboard
 * shortcut defaults to A to match the button order, and the chip renders
 * nothing at all.
 */
export function recommendedKeepSide(candidate: DedupCandidate): KeepRecommendation | null {
  const qA = metadataQuality(candidate.book_a);
  const qB = metadataQuality(candidate.book_b);
  if (qA > qB) return { keepId: candidate.entity_a_id, label: 'A' };
  if (qB > qA) return { keepId: candidate.entity_b_id, label: 'B' };
  return null;
}

/**
 * The id `m` merges on. Separated from `recommendedKeepSide` because a tie
 * still has to resolve to something, and burying that fallback inside the
 * recommendation would make the chip claim a recommendation exists when it
 * does not.
 */
export function keepIdForMerge(candidate: DedupCandidate): { keepId: string; label: 'A' | 'B' } {
  return recommendedKeepSide(candidate) ?? { keepId: candidate.entity_a_id, label: 'A' };
}
