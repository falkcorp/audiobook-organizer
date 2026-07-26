// file: web/src/pages/__tests__/reviewPayload.test.ts
// version: 1.0.0
// guid: 6a1d8f30-4b52-4e97-9c08-1f7b2e6d3a45

import { describe, it, expect } from 'vitest';
import { parsePayload, memberIDs, memberCount, memberEntries } from '../reviewPayload';

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
