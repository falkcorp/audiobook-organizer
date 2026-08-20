// file: web/src/components/review/reviewActions.test.ts
// version: 1.0.0
// guid: 9d4a7e21-05c8-4b63-a19f-3e8b2c60d475
// last-edited: 2026-08-20

import { describe, expect, it } from 'vitest';
import {
  affectedCount,
  inverseOf,
  isReversible,
  needsConfirmation,
  type ReviewAction,
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
      { lane: 'dupes', type: 'mergeAllFiltered' },
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
    expect(needsConfirmation({ lane: 'dupes', type: 'mergeAllFiltered' })).toBe(true);
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
    expect(affectedCount({ lane: 'dupes', type: 'mergeAllFiltered' })).toBeNull();
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
    // `mergeDedupCandidate(id: number)` -- which a shared `string | number` id
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
