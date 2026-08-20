// file: web/src/components/review/lanes/regroup.ts
// version: 1.0.0
// guid: 8e05b71c-3f62-4a08-9d54-1c7f2b6a0e39
// last-edited: 2026-08-20

import type { LaneDescriptor } from './types';

/**
 * The review-queue lane (holds awaiting a human decision).
 *
 * The only lane with no score at all. A recommendation here is reached by rules
 * over observed counts -- how many members, how many runtimes are known -- and
 * not by arithmetic on them, so its evidence is facts and there is nothing to
 * draw a bar from. Inventing one would be inventing a computation.
 *
 * "Approve" is not a synonym for the other lanes' "apply": an item can be
 * approved INTO one of several outcomes, chosen per row, so the verb has to stay
 * neutral about which one.
 */
export const regroupLane = {
  lane: 'regroup',
  label: 'Review queue',
  evidenceKind: 'facts',
  verbs: {
    approve: 'Approve',
    reject: 'Reject',
    bulk: 'Decide all of this kind',
  },
  emptyMessage: 'The review queue is empty. Nothing is waiting on a decision.',
} satisfies LaneDescriptor<'regroup'>;
