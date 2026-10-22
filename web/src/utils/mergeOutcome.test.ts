// file: web/src/utils/mergeOutcome.test.ts
// version: 1.0.0
// guid: 0c7d3a58-4e91-4b26-8f05-2a9e6d1b3c70
// last-edited: 2026-10-22

import { describe, expect, it } from 'vitest';
import { describeMergeOutcome } from './mergeOutcome';

const titles = { a: 'Dune', b: 'Dune (2nd copy)' };

describe('describeMergeOutcome', () => {
  it('reports a failed operation with its reason, not "Merged"', () => {
    const p = describeMergeOutcome({
      status: 'failed',
      errorMessage: 'book b is soft-deleted',
      result: null,
      requested: 1,
    });
    expect(p.severity).toBe('error');
    expect(p.message).toContain('did not complete');
    expect(p.message).toContain('book b is soft-deleted');
    expect(p.message).toContain('Nothing was merged');
  });

  it('names the skipped losers and their reason when some merged', () => {
    const p = describeMergeOutcome({
      status: 'completed',
      result: {
        keep_id: 'a',
        merged: 1,
        skipped: [{ book_id: 'b', reason: 'already in the trash' }],
        no_op: false,
      },
      requested: 2,
      titles,
    });
    expect(p.severity).toBe('warning');
    expect(p.message).toContain('Merged 1 book as versions.');
    expect(p.message).toContain('Skipped 1 of 2');
    expect(p.message).toContain('"Dune (2nd copy)" (already in the trash)');
  });

  it('an all-skipped merge is an info "nothing merged", with the reasons', () => {
    const p = describeMergeOutcome({
      status: 'completed',
      result: {
        keep_id: 'a',
        merged: 0,
        skipped: [{ book_id: 'b', reason: 'already in the trash' }],
        no_op: true,
      },
      requested: 1,
      titles,
    });
    expect(p.severity).toBe('info');
    expect(p.message).toContain('Nothing was merged');
    expect(p.message).toContain('already in the trash');
  });

  it('a clean merge is a success; a missing result is a warning, not a success', () => {
    expect(
      describeMergeOutcome({
        status: 'completed',
        result: { keep_id: 'a', merged: 2, no_op: false },
        requested: 2,
      })
    ).toEqual({ severity: 'success', message: 'Merged 2 books as versions.' });
    expect(describeMergeOutcome({ status: 'completed', result: null, requested: 2 }).severity).toBe(
      'warning'
    );
  });

  it('falls back to the id when a title is unknown', () => {
    const p = describeMergeOutcome({
      status: 'completed',
      result: { keep_id: 'a', merged: 0, skipped: [{ book_id: 'zz', reason: 'r' }], no_op: true },
      requested: 1,
    });
    expect(p.message).toContain('"zz" (r)');
  });
});
