// file: web/src/components/review/lanes/authors.ts
// version: 1.1.0
// guid: 4d7a2e91-c3b5-4f08-9a16-e82b05d3c7f4
// last-edited: 2026-10-13

import type { LaneDescriptor } from './types';

/**
 * The Authors & series lane.
 *
 * Its sub-views are Authors (groups of author names that look like one person,
 * merged under a canonical spelling) and Series (series that share a name, merged
 * into one). AI scans joins the same lane in a later change.
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
    // The Series sub-view. Distinct names from the author verbs on purpose (see
    // SeriesEntityAction): a series merge folds series ROWS together and moves
    // their books, which is not what "merge into the canonical author" says.
    mergeSeriesGroup: 'Merge the ticked series into the first one',
    mergeSelectedSeries: 'Merge selected series groups',
    mergeAllSeriesGroups: 'Merge all same-name series groups',
    renameSeries: 'Rename this series (can be undone)',
    undoRenameSeries: 'Undo the series rename',
    toggleSeriesNarrator: "Switch this series' credited author between author and narrator",
    toggleSeriesIncluded: 'Leave this series out of the merge, or put it back',
  },
  emptyMessage: 'No duplicate authors were found. Nothing is waiting on a decision.',
} satisfies LaneDescriptor<'authors'>;
