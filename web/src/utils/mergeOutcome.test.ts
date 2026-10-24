// file: web/src/utils/mergeOutcome.test.ts
// version: 1.1.0
// guid: 0c7d3a58-4e91-4b26-8f05-2a9e6d1b3c70
// last-edited: 2026-10-22

import { describe, expect, it } from 'vitest';
import { describeMergeOutcome } from './mergeOutcome';

const titles = { a: 'Dune', b: 'Dune (2nd copy)' };

describe('describeMergeOutcome', () => {
  it('a failed op with no stored result never claims nothing was merged', () => {
    const p = describeMergeOutcome({
      status: 'failed',
      errorMessage: 'book b is soft-deleted',
      result: null,
      requested: 1,
    });
    expect(p.severity).toBe('error');
    expect(p.message).toContain('did not complete (failed): book b is soft-deleted');
    expect(p.message).toContain('Check the operation to see what it did.');
    expect(p.message).not.toContain('Nothing was merged');
  });

  it('a cancelled op without a result is the same neutral outcome', () => {
    const p = describeMergeOutcome({ status: 'cancelled', result: null, requested: 2 });
    expect(p.severity).toBe('error');
    expect(p.message).toBe(
      'The merge did not complete (cancelled). Check the operation to see what it did.'
    );
  });

  it('a partial failure says it may have partly applied', () => {
    const p = describeMergeOutcome({
      status: 'failed',
      errorMessage: 'op error',
      result: {
        keep_id: 'a',
        merged: 0,
        failed: true,
        partial: true,
        error: 'clean-up of b failed',
      },
      requested: 2,
    });
    expect(p.severity).toBe('error');
    expect(p.message).toContain(
      'The merge failed part-way and may have partly applied; see the operation for what changed.'
    );
    expect(p.message).toContain('clean-up of b failed');
    expect(p.message).not.toContain('Nothing was merged');
  });

  it('a refusal before any write says nothing was merged, with the reason', () => {
    const p = describeMergeOutcome({
      status: 'failed',
      result: {
        keep_id: 'a',
        merged: 0,
        failed: true,
        partial: false,
        error: 'keep book is trashed',
      },
      requested: 1,
    });
    expect(p.severity).toBe('error');
    expect(p.message).toBe(
      'The merge was refused before changing anything: keep book is trashed. Nothing was merged.'
    );
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
