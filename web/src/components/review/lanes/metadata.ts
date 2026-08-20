// file: web/src/components/review/lanes/metadata.ts
// version: 1.0.0
// guid: c4a08f26-5b71-4d39-92e0-6f3b1a7c58d2
// last-edited: 2026-08-20

import type { LaneDescriptor } from './types';

/**
 * The metadata-match lane.
 *
 * This is the lane the scoring instrumentation was built for: its candidates
 * carry a recorded `score_breakdown`, and `metadataEvidence` replays it as a
 * waterfall. See docs/evidence-panel-audit.md for why it cannot use the dedup
 * lane's share bar -- its score is a product, not a sum.
 *
 * Reject and skip are the two undoable actions in the whole workspace; their
 * verbs are worded so the undo affordance reads as expected rather than as a
 * second, different action.
 */
export const metadataLane = {
  lane: 'metadata',
  label: 'Metadata',
  evidenceKind: 'waterfall',
  verbs: {
    apply: 'Apply',
    applySelected: 'Apply selected',
    reject: 'Reject match',
    unreject: 'Undo reject',
    skip: 'Skip for now',
    unskip: 'Undo skip',
    // A group is several books competing for one match. Rejecting the group
    // rejects the match for all of them, which is not the same as rejecting each
    // book's own best match one at a time.
    rejectGroup: 'Reject for this whole group',
    skipAllUnmatched: 'Skip everything without a match',
    // Takes a book out of its ambiguity group so it can be decided alone.
    ungroup: 'Separate from group',
  },
  emptyMessage:
    'No metadata matches to review. Search providers from the Metadata menu to find some.',
} satisfies LaneDescriptor<'metadata'>;
