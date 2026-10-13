// file: web/src/components/review/lanes/authors.ts
// version: 1.0.0
// guid: 4d7a2e91-c3b5-4f08-9a16-e82b05d3c7f4
// last-edited: 2026-10-13

import type { LaneDescriptor } from './types';

/**
 * The Authors & series lane.
 *
 * Its first sub-view is Authors: groups of author names that look like one
 * person, merged under a canonical spelling. The Series and AI scans sub-views
 * join the same lane in later changes, which is why the label already says
 * "Authors & series" while only one of the three is here.
 *
 * `facts` evidence, like the regroup lane: a group is reached by comparing name
 * forms, and there is no score to draw a bar from. Inventing one would be
 * inventing a computation.
 *
 * Every verb is mandatory (the map is typed over the lane's own actions), and
 * each says what the click does to the AUTHOR rows rather than to a candidate:
 * "Merge" folds variant author records into the canonical one, and that cannot
 * be undone from this screen.
 */
export const authorsLane = {
  lane: 'authors',
  label: 'Authors & series',
  evidenceKind: 'facts',
  verbs: {
    mergeGroup: 'Merge into the canonical author',
    mergeSelected: 'Merge selected groups',
    mergeAllGroups: 'Merge all groups',
    renameCanonical: 'Rename the canonical author',
    splitComposite: 'Split into separate authors',
    resolveProduction: 'Find the real author',
    toggleRole: 'Switch between author and narrator',
    excludeVariant: 'Leave this variant out of the merge, or put it back',
  },
  emptyMessage: 'No duplicate authors were found. Nothing is waiting on a decision.',
} satisfies LaneDescriptor<'authors'>;
