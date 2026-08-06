// file: web/src/lib/__tests__/reviewPayload.test.ts
// version: 1.1.0
// guid: 6a1d8f30-4b52-4e97-9c08-1f7b2e6d3a45
// last-edited: 2026-08-06

import { describe, it, expect } from 'vitest';
import {
  ACTION_INSUFFICIENT_EVIDENCE,
  REVIEW_ACTIONS,
  actionSpec,
  defaultActionFor,
  evidenceFacts,
  humanRuntime,
  labelForAction,
  memberCount,
  memberEntries,
  memberIDs,
  parsePayload,
} from '../reviewPayload';

// The Go producer (buildRegroupPayload) writes exactly this shape. These tests pin the
// key names so the camelCase/snake_case mismatch that silently broke the old
// member-count/enrichment can never regress.
const producerPayload = JSON.stringify({
  folder: '/lib/When We Were Sisters',
  files: [
    '/lib/When We Were Sisters/When We Were Sisters_1.mp3',
    '/lib/When We Were Sisters/When We Were Sisters_2.mp3',
    '/lib/When We Were Sisters/When We Were Sisters_3.mp3',
  ],
  proposedAction: 'collapse flat multi-track folder into 1 multi-file audiobook',
  memberBookIDs: ['b1', 'b2', 'b3'],
  survivorTitle: 'When We Were Sisters',
  confidence: 'high',
  discNumbers: [0, 0, 0],
  trackNumbers: [1, 2, 3],
});

describe('review payload parsing', () => {
  it('parsePayload returns null on empty / malformed input', () => {
    expect(parsePayload('')).toBeNull();
    expect(parsePayload('{not json')).toBeNull();
  });

  it('memberIDs reads the producer camelCase key', () => {
    const p = parsePayload(producerPayload);
    expect(memberIDs(p)).toEqual(['b1', 'b2', 'b3']);
  });

  it('memberIDs falls back to the snake_case alias', () => {
    const p = parsePayload(JSON.stringify({ member_ids: ['x', 'y'] }));
    expect(memberIDs(p)).toEqual(['x', 'y']);
  });

  it('memberCount uses member IDs, not the files fallback, for the real payload', () => {
    const p = parsePayload(producerPayload);
    // Regression guard: the old code read member_ids and silently fell through to
    // files.length because the producer emits memberBookIDs. Both are length 3 here,
    // but memberCount must resolve via IDs first.
    expect(memberCount(p)).toBe(3);
  });

  it('memberEntries zips files/ids/disc/track index-aligned', () => {
    const p = parsePayload(producerPayload);
    const entries = memberEntries(p);
    expect(entries).toHaveLength(3);
    expect(entries[0]).toEqual({
      filePath: '/lib/When We Were Sisters/When We Were Sisters_1.mp3',
      bookId: 'b1',
      disc: 0,
      track: 1,
    });
    // Flat same-disc chapters: disc 0, contiguous tracks.
    expect(entries.map((e) => e.disc)).toEqual([0, 0, 0]);
    expect(entries.map((e) => e.track)).toEqual([1, 2, 3]);
  });

  it('memberEntries preserves real disc numbers for a genuine disc set', () => {
    const p = parsePayload(
      JSON.stringify({
        files: ['/x/Disc 1/t.mp3', '/x/Disc 2/t.mp3'],
        memberBookIDs: ['d1', 'd2'],
        discNumbers: [1, 2],
        trackNumbers: [1, 1],
      })
    );
    const entries = memberEntries(p);
    expect(entries.map((e) => e.disc)).toEqual([1, 2]);
    expect(entries.map((e) => e.track)).toEqual([1, 1]);
  });

  it('memberEntries tolerates a legacy payload with no disc/track arrays', () => {
    const p = parsePayload(
      JSON.stringify({ files: ['/x/a.mp3'], memberBookIDs: ['b1'] })
    );
    const entries = memberEntries(p);
    expect(entries).toHaveLength(1);
    expect(entries[0].disc).toBeUndefined();
    expect(entries[0].track).toBeUndefined();
  });
});

// ═══ Recommendation, evidence, and the default action ════════════════════════════
//
// These pin the two rules the backend enforces on its side, so the UI and the API
// can never disagree about what is offerable:
//   1. `insufficient-evidence` is NOT an approvable choice — it is a statement BY the
//      classifier, and approving with it is a deliberate 400.
//   2. Absent evidence is not zero evidence — a hold with no evidence block must read
//      as "nothing recorded", never as a row of confident-looking zeros.
describe('review recommendations', () => {
  it('defaultActionFor preselects an approvable recommendation', () => {
    const p = parsePayload(JSON.stringify({ recommendedAction: 'combine' }));
    expect(defaultActionFor(p)).toBe('combine');
    expect(defaultActionFor(parsePayload(JSON.stringify({ recommendedAction: 'separate' }))))
      .toBe('separate');
  });

  it('defaultActionFor preselects NOTHING for insufficient-evidence', () => {
    // The backend 400s this action on purpose, and guessing a default here would
    // guess `combine` on exactly the holds with the least evidence.
    const p = parsePayload(JSON.stringify({ recommendedAction: ACTION_INSUFFICIENT_EVIDENCE }));
    expect(defaultActionFor(p)).toBe('');
  });

  it('defaultActionFor preselects NOTHING for a pre-recommendation hold', () => {
    // Every hold currently in prod's queue: no recommendedAction at all.
    expect(defaultActionFor(parsePayload('{}'))).toBe('');
    expect(defaultActionFor(null)).toBe('');
  });

  it('defaultActionFor preselects NOTHING for an action outside the vocabulary', () => {
    const p = parsePayload(JSON.stringify({ recommendedAction: 'seperate' }));
    expect(defaultActionFor(p)).toBe('');
  });

  it('insufficient-evidence is in the vocabulary but is never approvable', () => {
    const spec = actionSpec(ACTION_INSUFFICIENT_EVIDENCE);
    expect(spec).toBeDefined();
    expect(spec?.approvable).toBe(false);
    expect(REVIEW_ACTIONS.filter((a) => a.approvable).map((a) => a.value)).toEqual([
      'combine',
      'separate',
      'version-group',
      'duplicate-of',
    ]);
  });

  it('duplicate-of is offered and flagged unimplemented rather than hidden', () => {
    // The backend answers 501. Hiding the option would misrepresent the vocabulary;
    // faking success would mark a hold decided while doing nothing.
    expect(actionSpec('duplicate-of')?.unimplemented).toBe(true);
    expect(actionSpec('duplicate-of')?.approvable).toBe(true);
  });

  it('evidenceFacts returns [] when a hold carries no evidence block', () => {
    expect(evidenceFacts(undefined)).toEqual([]);
    expect(evidenceFacts({})).toEqual([]);
    expect(evidenceFacts({ members: 0 })).toEqual([]);
  });

  it('evidenceFacts surfaces the numbers a reviewer needs', () => {
    const labels = evidenceFacts({
      members: 6,
      durationsKnown: 5,
      bookLengthMembers: 4,
      medianKnownSec: 40000,
      longestKnownSec: 56880,
      distinctStems: 6,
      numberedMembers: 6,
      structure: 'flat',
    }).map((f) => f.label);
    expect(labels).toContain('6 members');
    expect(labels).toContain('5/6 runtimes known');
    expect(labels).toContain('4 book-length');
    expect(labels).toContain('longest 15.8 h');
    expect(labels).toContain('6 distinct titles');
    expect(labels).toContain('flat');
  });

  it('evidenceFacts flags the known-runtime gap that blocks a decisive call', () => {
    // One known runtime among five members is why a recommendation lands on
    // insufficient-evidence, so that chip has to stand out.
    const gap = evidenceFacts({ members: 5, durationsKnown: 1 }).find((f) =>
      f.label.includes('runtimes known')
    );
    expect(gap?.warn).toBe(true);
    const fine = evidenceFacts({ members: 5, durationsKnown: 4 }).find((f) =>
      f.label.includes('runtimes known')
    );
    expect(fine?.warn).toBe(false);
  });

  it('humanRuntime reads as hours for a book and minutes for a chapter', () => {
    expect(humanRuntime(56880)).toBe('15.8 h');
    expect(humanRuntime(2400)).toBe('40 min');
    expect(humanRuntime(0)).toBe('—');
    expect(humanRuntime(undefined)).toBe('—');
  });

  it('labelForAction falls back to the raw value for an unknown action', () => {
    expect(labelForAction('combine')).toBe('Combine');
    expect(labelForAction('some-future-action')).toBe('some-future-action');
    expect(labelForAction(undefined)).toBe('');
  });
});
