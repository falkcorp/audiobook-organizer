// file: web/src/utils/purgeSummary.test.ts
// version: 1.2.0
// guid: 5b2e8f13-9c47-4d60-a3e1-8f0d6b4a7c25
// last-edited: 2026-10-22

import { describe, expect, it } from 'vitest';
import type { PurgeResult } from '../services/api';
import {
  decidedCount,
  errorDetails,
  keptReasons,
  keptReasonsOfResult,
  outcomeHeadline,
  outcomeNotes,
  outcomeSeverity,
  monotonicProgress,
  previewHeadline,
  previewNotes,
  progressPercent,
} from './purgeSummary';

const base: PurgeResult = { attempted: 0, purged: 0, files_deleted: 0, errors: [] };

describe('previewHeadline', () => {
  it('says how many a purge would remove out of the trash', () => {
    expect(previewHeadline({ ...base, attempted: 50, purgeable: 12 })).toBe(
      'A purge would remove 12 books of the 50 in the trash right now.'
    );
  });
  it('says none when none is purgeable, not that it will purge the trash', () => {
    expect(previewHeadline({ ...base, attempted: 50, purgeable: 0 })).toBe(
      'A purge would remove none of the 50 books in the trash right now.'
    );
  });
  it('handles an empty trash and the singular', () => {
    expect(previewHeadline(base)).toBe('There are no soft-deleted books to purge.');
    expect(previewHeadline({ ...base, attempted: 3, purgeable: 1 })).toContain('remove 1 book of');
  });
});

describe('outcomeHeadline', () => {
  it('reports removed out of attempted, with files', () => {
    expect(outcomeHeadline({ ...base, attempted: 10, purged: 4, files_deleted: 3 })).toBe(
      'Purged 4 of 10 soft-deleted books, and deleted 3 files from disk.'
    );
  });
  it('a purge that removed nothing reads as none, never as success', () => {
    expect(outcomeHeadline({ ...base, attempted: 10, purged: 0 })).toBe(
      'Purged none of the 10 books in the trash.'
    );
  });
});

describe('kept reasons', () => {
  it('lists only the reasons with a count, in order', () => {
    const r = keptReasonsOfResult({
      ...base,
      skipped_owns_files: 7,
      kept_undo_possible: 2,
      kept_has_progress: 0,
    });
    expect(r.map((x) => [x.key, x.count])).toEqual([
      ['owns_files', 7],
      ['undo_possible', 2],
    ]);
    expect(r[1].label).toContain('can still be undone');
  });
  it('is empty when nothing is kept', () => {
    expect(keptReasons({ ownsFiles: 0, undoPossible: 0, hasProgress: 0 })).toEqual([]);
  });
  it('uses singular and plural verbs', () => {
    expect(keptReasons({ ownsFiles: 1, undoPossible: 0, hasProgress: 0 })[0].label).toMatch(
      /^still owns /
    );
    expect(keptReasons({ ownsFiles: 2, undoPossible: 0, hasProgress: 0 })[0].label).toMatch(
      /^still own /
    );
  });
});

describe('outcome severity and notes', () => {
  it('warns when something failed or errored, otherwise succeeds', () => {
    expect(outcomeSeverity(base)).toBe('success');
    expect(outcomeSeverity({ ...base, failed: 1 })).toBe('warning');
    expect(outcomeSeverity({ ...base, carry_failed: 1 })).toBe('warning');
    expect(outcomeSeverity({ ...base, errors: ['x'] })).toBe('warning');
  });
  it('"purged none of N" is a warning, not a success (N-d)', () => {
    expect(outcomeSeverity({ ...base, attempted: 10, purged: 0 })).toBe('warning');
    expect(outcomeSeverity({ ...base, attempted: 10, purged: 1 })).toBe('success');
    expect(outcomeSeverity({ ...base, attempted: 0, purged: 0 })).toBe('success');
  });
  it('notes carried progress and failures', () => {
    const notes = outcomeNotes({ ...base, carried_to_sibling: 2, carry_failed: 1, failed: 3 });
    expect(notes).toHaveLength(3);
    expect(notes[0]).toContain('2 books had listening progress');
    expect(notes[1]).toContain('1 book; it was kept');
    expect(notes[2]).toContain('3 books could not be decided');
  });
});

describe('a purge that stopped early', () => {
  const stopped: PurgeResult = {
    ...base,
    attempted: 100,
    purged: 12,
    files_deleted: 4,
    skipped_owns_files: 20,
    kept_has_progress: 3,
    failed: 1,
    stopped_early: true,
    stop_reason: 'cancelled',
  };
  it('headline says it stopped early, why, and the exact counts decided so far', () => {
    expect(outcomeHeadline(stopped)).toBe(
      'Stopped early (cancelled): purged 12 books, 4 files deleted from disk; 36 of 100 books in the trash were decided before it stopped.'
    );
    expect(decidedCount(stopped)).toBe(36);
  });
  it('cancelled is a warning; a timeout or other stop is an error', () => {
    expect(outcomeSeverity(stopped)).toBe('warning');
    expect(outcomeSeverity({ ...stopped, stop_reason: 'timed out' })).toBe('error');
    expect(outcomeSeverity({ ...stopped, stop_reason: undefined })).toBe('error');
    expect(outcomeHeadline({ ...stopped, stop_reason: undefined })).toContain(
      'Stopped early (unknown reason)'
    );
  });
  it('a stopped result with nothing purged never reads as success', () => {
    const r = { ...stopped, purged: 0, files_deleted: 0, stop_reason: 'timed out' };
    expect(outcomeHeadline(r)).toMatch(/^Stopped early \(timed out\): purged 0 books;/);
  });
  it('a stopped preview says it covers only part of the trash', () => {
    expect(
      previewHeadline({
        ...base,
        attempted: 10,
        purgeable: 2,
        stopped_early: true,
        stop_reason: 'timed out',
      })
    ).toContain('Stopped early (timed out): the check covered only part of the trash');
  });
});

describe('undecided books are named, not hidden', () => {
  it('a preview with failed books says how many could not be decided', () => {
    const r: PurgeResult = {
      ...base,
      attempted: 10,
      purgeable: 5,
      skipped_owns_files: 2,
      failed: 2,
      carry_failed: 1,
      errors: ['b1: x', 'b2: y'],
    };
    const notes = previewNotes(r);
    expect(notes).toHaveLength(2);
    expect(notes[0]).toContain('1 book has listening progress that could not be moved');
    expect(notes[1]).toContain('2 books could not be decided');
    // purgeable + owns + undo + progress + carry-failed + failed = attempted
    expect(decidedCount(r)).toBe(r.attempted);
  });
  it('a clean preview has no notes', () => {
    expect(previewNotes({ ...base, attempted: 3, purgeable: 3 })).toEqual([]);
  });
  it('caps the error lines at five and counts the rest', () => {
    const errors = Array.from({ length: 8 }, (_, i) => `e${i}`);
    const d = errorDetails({ ...base, failed: 8, errors });
    expect(d.shown).toEqual(['e0', 'e1', 'e2', 'e3', 'e4']);
    expect(d.more).toBe(3);
    expect(d.noDetails).toBe(false);
  });
  it('flags failed books that have no recorded details', () => {
    expect(errorDetails({ ...base, failed: 2 }).noDetails).toBe(true);
  });
});

describe('progress is monotonic', () => {
  const p = (done: number, total: number, message = 'm') => ({ done, total, message });
  it('never moves counters backwards, always takes the latest message', () => {
    const first = monotonicProgress(null, p(20, 40, 'a'));
    const second = monotonicProgress(first, p(10, 30, 'b'));
    expect(second).toEqual({ done: 20, total: 40, message: 'b' });
    expect(monotonicProgress(second, p(25, 40, ''))).toEqual({ done: 25, total: 40, message: 'b' });
  });
  it('the bar value is clamped to 0..100 and null without a total', () => {
    expect(progressPercent(null)).toBeNull();
    expect(progressPercent(p(1, 0))).toBeNull();
    expect(progressPercent(p(50, 40))).toBe(100);
    expect(progressPercent(p(10, 40))).toBe(25);
  });
});
