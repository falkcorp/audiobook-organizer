// file: web/src/components/review/lanes/lanes.test.ts
// version: 1.0.0
// guid: b6d29a04-8f35-4c71-90e2-3a5f7c1b8046
// last-edited: 2026-08-20

import { describe, expect, it } from 'vitest';
import { LANES, LANE_ORDER, dupesLane, metadataLane, regroupLane } from './index';
import type { LaneDescriptor } from './types';
import type { ActionForLane, ReviewAction, ReviewLane } from '../reviewActions';

describe('lane registry', () => {
  it('covers every lane in the union', () => {
    // LANES is typed as Record<ReviewLane, ...>, so a missing lane is a compile
    // error. This checks the runtime side of the same claim -- that the object
    // literal was not satisfied by a cast somewhere.
    const lanes: ReviewLane[] = ['dupes', 'metadata', 'regroup'];
    for (const lane of lanes) {
      expect(LANES[lane], `no descriptor for lane "${lane}"`).toBeDefined();
      expect(LANES[lane].lane).toBe(lane);
    }
  });

  it('orders every lane exactly once', () => {
    expect([...LANE_ORDER].sort()).toEqual(Object.keys(LANES).sort());
    expect(new Set(LANE_ORDER).size).toBe(LANE_ORDER.length);
  });
});

describe('action vocabulary is total', () => {
  // The failure this prevents: an action bar rendering a button labelled
  // "applySelected" because the verb map was a partial lookup with a fallback.
  // Every action a lane can produce must have a human phrase.
  const cases: Array<{ lane: ReviewLane; types: string[] }> = [
    {
      lane: 'dupes',
      types: ['merge', 'dismiss', 'mergeSelected', 'dismissSelected', 'mergeAllFiltered'],
    },
    {
      lane: 'metadata',
      types: [
        'apply',
        'applySelected',
        'reject',
        'unreject',
        'skip',
        'unskip',
        'rejectGroup',
        'skipAllUnmatched',
        'ungroup',
      ],
    },
    { lane: 'regroup', types: ['approve', 'reject', 'bulk'] },
  ];

  for (const { lane, types } of cases) {
    it(`names every ${lane} action`, () => {
      const verbs = LANES[lane].verbs as Record<string, string>;
      expect(Object.keys(verbs).sort()).toEqual([...types].sort());
      for (const [type, phrase] of Object.entries(verbs)) {
        expect(phrase, `${lane}/${type} has no phrase`).toBeTruthy();
        // A verb that is just the action name is the fallback this map exists to
        // replace -- it means someone added the key to silence the compiler.
        expect(phrase, `${lane}/${type} is not a human phrase`).not.toBe(type);
      }
    });
  }

  it('fails to compile when a lane gains an action without a verb', () => {
    const incomplete: LaneDescriptor<'metadata'> = {
      lane: 'metadata',
      label: 'Metadata',
      evidenceKind: 'waterfall',
      // @ts-expect-error -- 'ungroup' is missing, and the Record over the lane's
      // own action types is what catches it. If this directive ever reports
      // "unused", the exhaustiveness guarantee has been lost and every lane's
      // vocabulary is silently partial again.
      verbs: {
        apply: 'Apply',
        applySelected: 'Apply selected',
        reject: 'Reject match',
        unreject: 'Undo reject',
        skip: 'Skip for now',
        unskip: 'Undo skip',
        rejectGroup: 'Reject for this whole group',
        skipAllUnmatched: 'Skip everything without a match',
      },
      emptyMessage: '',
    };
    expect(incomplete).toBeDefined();
  });

  it('rejects a verb for an action belonging to another lane', () => {
    const verbs = dupesLane.verbs as Record<string, string>;
    // Type-level: 'apply' is not a dupes action. Runtime: confirm it really is
    // absent, so the two levels agree.
    expect(verbs.apply).toBeUndefined();
    expect((metadataLane.verbs as Record<string, string>).merge).toBeUndefined();
  });
});

describe('evidence kind matches the lane arithmetic', () => {
  // Fixed per lane on purpose -- see ../evidence/types.ts. A lane whose kind
  // changed would be asserting something different about how its score is
  // computed, which is a backend change, not a display tweak.
  it('assigns each lane the encoding its arithmetic supports', () => {
    expect(dupesLane.evidenceKind).toBe('weighted'); // weighted sum
    expect(metadataLane.evidenceKind).toBe('waterfall'); // product + terms
    expect(regroupLane.evidenceKind).toBe('facts'); // no score at all
  });
});

describe('vocabulary distinguishes what the lanes actually mean', () => {
  it('does not use one word for two different claims', () => {
    // Dedup "dismiss" says the pair is not a duplicate and retires the candidate.
    // Metadata "reject" says this one proposed match is wrong and leaves the book
    // queued for a better one. Sharing a verb would flatten that.
    expect(dupesLane.verbs.dismiss).not.toBe(metadataLane.verbs.reject);
    expect(dupesLane.verbs.dismiss).toMatch(/not a duplicate/i);
    expect(metadataLane.verbs.reject).toMatch(/match/i);
  });

  it('names the filter-scoped merge by its scope', () => {
    // "Merge all" would read as "merge the whole library". This one acts on the
    // current filter, and the reviewer has usually just narrowed it.
    expect(dupesLane.verbs.mergeAllFiltered).toMatch(/filter/i);
  });
});

// Type-level sanity: ActionForLane really does narrow, which is what makes the
// verb Record total per lane rather than over the whole union.
const _dupesOnly: ActionForLane<'dupes'>['type'] = 'mergeAllFiltered';
// @ts-expect-error -- 'apply' belongs to the metadata lane
const _crossed: ActionForLane<'dupes'>['type'] = 'apply';
const _anyAction: ReviewAction = { lane: 'dupes', type: 'merge', id: 1 };
void _dupesOnly;
void _crossed;
void _anyAction;
