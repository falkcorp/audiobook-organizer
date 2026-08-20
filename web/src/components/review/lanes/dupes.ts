// file: web/src/components/review/lanes/dupes.ts
// version: 1.0.0
// guid: 7b1d4e93-2c05-48a6-b39f-0e6a2c85f174
// last-edited: 2026-08-20

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
  // The dedup score is a weighted sum of calibrated signals, so it is the one
  // lane entitled to a stacked share bar.
  evidenceKind: 'weighted',
  verbs: {
    merge: 'Merge',
    dismiss: 'Not a duplicate',
    mergeSelected: 'Merge selected',
    dismissSelected: 'Dismiss selected',
    // Named for its scope, not its effect. "Merge all" reads as "merge
    // everything"; this acts on the current filter, and a reviewer who has
    // narrowed to one author must not think it is about to touch the library.
    mergeAllFiltered: 'Merge everything matching this filter',
  },
  emptyMessage: 'No duplicate candidates. Run a scan from the Dedup menu to look for more.',
} satisfies LaneDescriptor<'dupes'>;
