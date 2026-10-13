// file: web/src/components/review/reviewActions.test.ts
// version: 1.7.0
// guid: 9d4a7e21-05c8-4b63-a19f-3e8b2c60d475
// last-edited: 2026-10-13

import { describe, expect, it } from 'vitest';
import {
  affectedCount,
  asAuthorId,
  asSeriesId,
  inverseOf,
  isReversible,
  needsConfirmation,
  type AuthorsAction,
  type AuthorEntityAction,
  type ReviewAction,
  type SeriesEntityAction,
  type SeriesId,
} from './reviewActions';

describe('reversibility', () => {
  // The reviewer is offered "click to undo" on exactly these. Offering it
  // anywhere else promises an inverse that no endpoint implements.
  it('round-trips the metadata actions that render as undoable chips', () => {
    const reject: ReviewAction = { lane: 'metadata', type: 'reject', id: 'b1' };
    const skip: ReviewAction = { lane: 'metadata', type: 'skip', id: 'b1' };

    expect(inverseOf(reject)).toEqual({ lane: 'metadata', type: 'unreject', id: 'b1' });
    expect(inverseOf(skip)).toEqual({ lane: 'metadata', type: 'unskip', id: 'b1' });

    // Undoing an undo returns the original, so the chip can toggle indefinitely
    // rather than working once.
    expect(inverseOf(inverseOf(reject)!)).toEqual(reject);
    expect(inverseOf(inverseOf(skip)!)).toEqual(skip);
  });

  it('does not claim an inverse for destructive or fan-out actions', () => {
    const irreversible: ReviewAction[] = [
      { lane: 'dupes', type: 'merge', id: 1 },
      { lane: 'dupes', type: 'dismiss', id: 1 },
      { lane: 'dupes', type: 'mergeAllFiltered', expectedTotal: 3 },
      { lane: 'metadata', type: 'apply', id: 'b1' },
      { lane: 'metadata', type: 'skipAllUnmatched' },
      { lane: 'regroup', type: 'bulk', kind: 'series', decision: 'approve' },
    ];
    for (const action of irreversible) {
      expect(inverseOf(action), `${action.lane}/${action.type}`).toBeNull();
      expect(isReversible(action)).toBe(false);
    }
  });

  it('does not treat a regroup reject as a metadata reject', () => {
    // Both lanes have an action literally named "reject" and only one of them is
    // undoable. Switching on `type` alone -- the obvious implementation -- would
    // hand the review queue an undo chip wired to a metadata endpoint.
    expect(inverseOf({ lane: 'regroup', type: 'reject', id: 'r1' })).toBeNull();
    expect(inverseOf({ lane: 'metadata', type: 'reject', id: 'b1' })).not.toBeNull();
  });
});

describe('confirmation', () => {
  it('confirms merges and fan-out decisions', () => {
    expect(needsConfirmation({ lane: 'dupes', type: 'merge', id: 1 })).toBe(true);
    expect(needsConfirmation({ lane: 'dupes', type: 'mergeSelected', ids: [1, 2] })).toBe(true);
    expect(needsConfirmation({ lane: 'dupes', type: 'mergeAllFiltered', expectedTotal: 3 })).toBe(true);
    expect(
      needsConfirmation({ lane: 'regroup', type: 'bulk', kind: 'k', decision: 'reject' })
    ).toBe(true);
  });

  it('does not confirm single reversible actions', () => {
    // Confirming everything is the same as confirming nothing: the reviewer
    // learns to dismiss the dialog without reading it.
    expect(needsConfirmation({ lane: 'metadata', type: 'reject', id: 'b1' })).toBe(false);
    expect(needsConfirmation({ lane: 'metadata', type: 'skip', id: 'b1' })).toBe(false);
    expect(needsConfirmation({ lane: 'dupes', type: 'dismiss', id: 1 })).toBe(false);
    expect(needsConfirmation({ lane: 'regroup', type: 'approve', id: 'r1' })).toBe(false);
  });

  it('confirms a multi-row dismissal but not a single one', () => {
    expect(needsConfirmation({ lane: 'dupes', type: 'dismissSelected', ids: [1] })).toBe(false);
    expect(needsConfirmation({ lane: 'dupes', type: 'dismissSelected', ids: [1, 2] })).toBe(true);
  });
});

describe('cluster actions', () => {
  it('confirms a cluster link and a cluster reject, but not a single-book removal', () => {
    expect(needsConfirmation({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2'] })).toBe(true);
    expect(
      needsConfirmation({ lane: 'dupes', type: 'rejectCluster', ids: ['b1', 'b2'], candidateIds: [1] })
    ).toBe(true);
    expect(
      needsConfirmation({ lane: 'dupes', type: 'removeFromCluster', ids: ['b1', 'b2', 'b3'], removeIds: ['b3'] })
    ).toBe(false);
  });

  it('offers no undo for any of them', () => {
    expect(inverseOf({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2'] })).toBeNull();
    expect(inverseOf({ lane: 'dupes', type: 'rejectCluster', ids: ['b1', 'b2'], candidateIds: [1] })).toBeNull();
    expect(
      inverseOf({ lane: 'dupes', type: 'removeFromCluster', ids: ['b1', 'b2'], removeIds: ['b2'] })
    ).toBeNull();
  });

  it('counts books for a link, the named pairs for a reject and the removed books for a removal', () => {
    expect(affectedCount({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2', 'b3'] })).toBe(3);
    expect(
      affectedCount({ lane: 'dupes', type: 'rejectCluster', ids: ['b1', 'b2', 'b3'], candidateIds: [4, 5] })
    ).toBe(2);
    expect(
      affectedCount({ lane: 'dupes', type: 'removeFromCluster', ids: ['b1', 'b2', 'b3'], removeIds: ['b3'] })
    ).toBe(1);
  });
});

describe('affectedCount', () => {
  it('counts what it can count', () => {
    expect(affectedCount({ lane: 'dupes', type: 'mergeSelected', ids: [1, 2, 3] })).toBe(3);
    expect(affectedCount({ lane: 'metadata', type: 'applySelected', ids: ['a', 'b'] })).toBe(2);
    expect(affectedCount({ lane: 'metadata', type: 'apply', id: 'a' })).toBe(1);
  });

  it('returns null rather than guessing for server-resolved target sets', () => {
    // A wrong number in a confirmation dialog is worse than no number, because
    // it is the number the reviewer uses to decide. These three resolve their
    // targets on the server against the live filter.
    expect(affectedCount({ lane: 'dupes', type: 'mergeAllFiltered', expectedTotal: 3 })).toBeNull();
    expect(affectedCount({ lane: 'metadata', type: 'skipAllUnmatched' })).toBeNull();
    expect(
      affectedCount({ lane: 'regroup', type: 'bulk', kind: 'k', decision: 'approve' })
    ).toBeNull();
  });
});

describe('lane id types', () => {
  it('keeps dedup ids numeric and the other lanes string, at the type level', () => {
    // This test is mostly a compile-time assertion; the runtime expectations are
    // incidental. The failure it guards is a metadata book id reaching
    // `linkDedupCandidate(id: number)` -- which a shared `string | number` id
    // would allow, and which no runtime test would catch until it 404s.
    const dupes: ReviewAction = { lane: 'dupes', type: 'merge', id: 42 };
    const metadata: ReviewAction = { lane: 'metadata', type: 'apply', id: 'book-42' };

    // @ts-expect-error -- a dedup candidate id is not a string
    const wrongDupes: ReviewAction = { lane: 'dupes', type: 'merge', id: 'book-42' };
    // @ts-expect-error -- a book id is not a number
    const wrongMetadata: ReviewAction = { lane: 'metadata', type: 'apply', id: 42 };

    expect(dupes.lane).toBe('dupes');
    expect(metadata.lane).toBe('metadata');
    // Referenced so the bindings are not flagged as unused; the assertions that
    // matter above are the @ts-expect-error directives, which fail the build if
    // the union ever stops rejecting these.
    expect(wrongDupes).toBeDefined();
    expect(wrongMetadata).toBeDefined();
  });

  it('rejects an action whose type belongs to a different lane', () => {
    // @ts-expect-error -- 'merge' is not in the metadata vocabulary
    const crossed: ReviewAction = { lane: 'metadata', type: 'merge', id: 'b1' };
    expect(crossed).toBeDefined();
  });
});

describe('repairs lane actions', () => {
  const rows: ReviewAction = {
    lane: 'repairs',
    type: 'applyRows',
    fixerId: 'vg',
    planOpId: 'plan-1',
    rowIds: ['a', 'b', 'c'],
  };
  const all: ReviewAction = {
    lane: 'repairs',
    type: 'applyAllApplicable',
    fixerId: 'vg',
    planOpId: 'plan-1',
  };
  const trial: ReviewAction = { lane: 'repairs', type: 'runTrial', fixerId: 'vg' };

  it('counts selected rows and does not guess the plan-wide count', () => {
    expect(affectedCount(rows)).toBe(3);
    expect(affectedCount(all)).toBeNull();
    expect(affectedCount(trial)).toBeNull();
  });

  it('leaves confirmation to the lane dispatch, so nothing asks twice', () => {
    // The lane confirms every apply itself (useRepairsLane's dispatch). An
    // ActionBar confirm on top would be a second prompt for one click.
    expect(needsConfirmation(rows)).toBe(false);
    expect(needsConfirmation(all)).toBe(false);
  });

  it('is irreversible from this screen', () => {
    expect(inverseOf(rows)).toBeNull();
    expect(inverseOf(all)).toBeNull();
  });
});

describe('authors lane actions', () => {
  const mergeSel: ReviewAction = {
    lane: 'authors',
    entity: 'author',
    type: 'mergeSelected',
    ids: [asAuthorId(1), asAuthorId(2), asAuthorId(3)],
  };
  const mergeAll: ReviewAction = { lane: 'authors', entity: 'author', type: 'mergeAllGroups', expectedIds: [1, 2, 3, 4, 5, 6, 7].map(asAuthorId) };

  it('knows its own counts: the selection, and the total the reviewer confirmed', () => {
    expect(affectedCount(mergeSel)).toBe(3);
    expect(affectedCount(mergeAll)).toBe(7);
  });

  it('confirms inside the lane dispatch, so nothing asks twice', () => {
    // The dupes lane's mergeSelected goes through the ActionBar and confirms;
    // the authors lane confirms a selection wider than the page (and Merge All)
    // in its own dispatch, so the shared answer is lane-aware.
    expect(needsConfirmation(mergeSel)).toBe(false);
    expect(needsConfirmation(mergeAll)).toBe(false);
    expect(needsConfirmation({ lane: 'dupes', type: 'mergeSelected', ids: [1, 2] })).toBe(true);
  });

  it('is irreversible from this screen', () => {
    expect(inverseOf(mergeSel)).toBeNull();
    expect(inverseOf(mergeAll)).toBeNull();
  });

  it('series id cannot be passed to mergeAuthors (type test)', () => {
    // Both id spaces are plain numbers on the wire. The brand is what makes
    // handing a series id to an author action a compile error; the directives
    // below fail the build if the union ever stops rejecting them.
    const seriesId = 12 as SeriesId;

    // @ts-expect-error -- a SeriesId is not an AuthorId
    const merge: AuthorsAction = { lane: 'authors', entity: 'author', type: 'mergeGroup', canonicalId: seriesId };
    // @ts-expect-error -- a bare number is not an AuthorId either: it must be branded at the edge
    const bare: AuthorsAction = { lane: 'authors', entity: 'author', type: 'mergeGroup', canonicalId: 12 };
    // @ts-expect-error -- an author action cannot name the series entity
    const wrongEntity: AuthorsAction = { lane: 'authors', entity: 'series', type: 'mergeGroup', canonicalId: asAuthorId(1) };

    expect([merge, bare, wrongEntity]).toHaveLength(3);
  });
});

describe('series actions (the Series sub-view)', () => {
  const mergeSel: ReviewAction = {
    lane: 'authors',
    entity: 'series',
    type: 'mergeSelectedSeries',
    groupKeys: ['s1-2', 's3-4', 's5-6'],
  };
  const mergeAll: ReviewAction = {
    lane: 'authors',
    entity: 'series',
    type: 'mergeAllSeriesGroups',
    expectedKeys: ['s1-2', 's3-4'],
  };

  it('knows its own counts, and confirms inside the lane like the authors lane', () => {
    expect(affectedCount(mergeSel)).toBe(3);
    expect(affectedCount(mergeAll)).toBe(2);
    expect(needsConfirmation(mergeSel)).toBe(false);
    expect(needsConfirmation(mergeAll)).toBe(false);
  });

  it('is irreversible from this screen, except the rename, which the lane undoes through the journal rather than here', () => {
    expect(inverseOf(mergeSel)).toBeNull();
    expect(inverseOf(mergeAll)).toBeNull();
    expect(inverseOf({ lane: 'authors', entity: 'series', type: 'renameSeries', groupKey: 's1-2', seriesId: asSeriesId(1), name: 'x' })).toBeNull();
  });

  it('an author id cannot be passed to a series action, nor a series id to an author one (type test)', () => {
    const authorId = asAuthorId(3);
    const seriesId = asSeriesId(12);

    // @ts-expect-error -- an AuthorId is not a SeriesId
    const rename: SeriesEntityAction = { lane: 'authors', entity: 'series', type: 'renameSeries', groupKey: 's1', seriesId: authorId, name: 'x' };
    // @ts-expect-error -- a bare number is not a SeriesId either: it must be branded at the edge
    const bare: SeriesEntityAction = { lane: 'authors', entity: 'series', type: 'toggleSeriesIncluded', groupKey: 's1', seriesId: 12 };
    // @ts-expect-error -- the narrator flag names an AUTHOR record
    const flag: SeriesEntityAction = { lane: 'authors', entity: 'series', type: 'toggleSeriesNarrator', groupKey: 's1', authorId: seriesId };
    // @ts-expect-error -- a series action cannot name the author entity
    const wrongEntity: SeriesEntityAction = { lane: 'authors', entity: 'author', type: 'mergeSeriesGroup', groupKey: 's1' };
    // @ts-expect-error -- and an author verb cannot be sent for the series entity
    const authorVerb: AuthorsAction = { lane: 'authors', entity: 'series', type: 'mergeGroup', canonicalId: authorId };
    // @ts-expect-error -- a series group has no canonical id: it is named by its group key
    const byAuthorShape: SeriesEntityAction = { lane: 'authors', entity: 'series', type: 'mergeSeriesGroup', canonicalId: authorId };
    // @ts-expect-error -- and an author action takes an author id, never a series id
    const crossed: AuthorEntityAction = { lane: 'authors', entity: 'author', type: 'mergeGroup', canonicalId: seriesId };

    expect([rename, bare, flag, wrongEntity, authorVerb, byAuthorShape, crossed]).toHaveLength(7);
  });
});
