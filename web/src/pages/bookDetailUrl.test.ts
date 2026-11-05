// file: web/src/pages/bookDetailUrl.test.ts
// version: 1.0.0
// guid: 7a14c9e2-5d38-4b60-92f1-0e3b8c6d5a74
// last-edited: 2026-11-05

import { describe, expect, it } from 'vitest';
import { parseSnapshotTs } from './bookDetailUrl';

describe('parseSnapshotTs', () => {
  it('keeps an RFC3339 instant', () => {
    expect(parseSnapshotTs('2026-03-01T00:00:00Z')).toBe('2026-03-01T00:00:00Z');
    expect(parseSnapshotTs('2026-03-01T10:15:30.123456+02:00')).toBe(
      '2026-03-01T10:15:30.123456+02:00'
    );
  });

  it('drops anything the server would answer 400 to', () => {
    for (const bad of [
      '',
      'tomorrow',
      '2026-03-01',
      '2026-03-01T00:00',
      '2026-13-45T00:00:00Z',
      null,
    ]) {
      expect(parseSnapshotTs(bad)).toBeNull();
    }
  });
});
