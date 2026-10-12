// file: web/src/components/review/lanes/dupes.ts
// version: 1.4.0
// guid: 7b1d4e93-2c05-48a6-b39f-0e6a2c85f174
// last-edited: 2026-10-12

import type { LaneDescriptor } from './types';

/**
 * The duplicate-candidate lane.
 *
 * Vocabulary note: this lane says "dismiss", not "reject". A dismissal asserts
 * the two books are NOT duplicates -- it is a judgement about the pair, and the
 * candidate is retired. The metadata lane's "reject" is a judgement about one
 * proposed match and leaves the book in the queue for a better one. Same button
 * position, different claim, so different word.
 */
export const dupesLane = {
  lane: 'dupes',
  label: 'Duplicates',
  // The dedup score is a noisy-OR product over calibrated per-signal
  // confidences plus bounded boosts, NOT a weighted sum -- this comment claimed
  // the latter, and claimed the lane was "the one lane entitled to a stacked
  // share bar", until 2026-09-01. A product has no decomposition into shares.
  // See dedupEvidence in ../evidence/adapters.ts for the formula.
  evidenceKind: 'confidence',
  verbs: {
    merge: 'Merge',
    dismiss: 'Not a duplicate',
    mergeSelected: 'Merge selected',
    dismissSelected: 'Dismiss selected',
    // Named for its scope, not its effect. "Merge all" reads as "merge
    // everything"; this acts on the current filter, and a reviewer who has
    // narrowed to one author must not think it is about to touch the library.
    mergeAllFiltered: 'Merge everything matching this filter',
    dismissAllFiltered: 'Dismiss everything matching this filter',
    // Side A is the older record: the pair is canonicalised with the smaller
    // ULID as A (internal/database/embedding_store.go). Owner decision D22: the
    // bulk keep-side is labelled older/newer; a server `recommended` is later.
    keepAAllFiltered: 'Keep the older record',
    keepBAllFiltered: 'Keep the newer record',
    // The Clusters view. A cluster is a set of books joined by pairs, so these
    // say "cluster" where the pair verbs say "merge"/"dismiss"; the wording of
    // the rest of the lane is "Not a duplicate", and "Reject cluster" is the
    // endpoint's own name for the same judgement on a whole group.
    linkCluster: 'Link cluster',
    rejectCluster: 'Reject cluster',
    removeFromCluster: 'Remove from cluster',
  },
  emptyMessage: 'No duplicate candidates. Run a scan from the Dedup menu to look for more.',
} satisfies LaneDescriptor<'dupes'>;
