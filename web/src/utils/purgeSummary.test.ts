// file: web/src/utils/purgeSummary.test.ts
// version: 1.6.0
// guid: 5b2e8f13-9c47-4d60-a3e1-8f0d6b4a7c25
// last-edited: 2026-10-25

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
  previewFiles,
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

describe('books trashed after the preview', () => {
  const r: PurgeResult = { ...base, attempted: 5, purged: 3, trashed_after_preview: 2 };
  it('counts as decided, so a finished pass adds up to the trash total', () => {
    expect(decidedCount(r)).toBe(5);
  });
  it('is named in the outcome notes, not hidden', () => {
    expect(outcomeNotes(r).join(' ')).toMatch(/2 books were put in the trash after the preview/);
    expect(outcomeNotes({ ...r, trashed_after_preview: 1 }).join(' ')).toMatch(
      /1 book was put in the trash after the preview/
    );
    expect(outcomeNotes({ ...r, trashed_after_preview: 0 })).toEqual([]);
  });
});

describe('books a preview could not list', () => {
  it('are named in the preview notes, so the numbers add up to the trash total', () => {
    const r: PurgeResult = {
      ...base,
      attempted: 7,
      purged: 0,
      purgeable: 4,
      skipped_owns_files: 1,
      trashed_after_preview: 2,
    };
    expect(decidedCount(r)).toBe(7);
    expect(previewNotes(r).join(' ')).toMatch(
      /2 books could not be listed: they were put in the trash while this preview ran, or have no record of when they were trashed\. They are left out/
    );
    expect(previewNotes({ ...r, trashed_after_preview: 1 }).join(' ')).toMatch(
      /1 book could not be listed: it was put in the trash while this preview ran, or has no record of when it was trashed\. It is left out/
    );
    expect(previewNotes({ ...r, trashed_after_preview: 0 })).toEqual([]);
  });
  it('are not described as trashed after a preview the owner confirmed', () => {
    const r: PurgeResult = { ...base, attempted: 3, purgeable: 2, trashed_after_preview: 1 };
    expect(previewNotes(r).join(' ')).not.toMatch(/after the preview you confirmed/);
  });
});

describe('books left alone by the window or the preview binding', () => {
  it('not-yet-due books count as decided, so a preview adds up to the trash total', () => {
    const r: PurgeResult = {
      ...base,
      attempted: 10,
      purged: 0,
      purgeable: 4,
      skipped_owns_files: 1,
      not_yet_due: 5,
    };
    expect(decidedCount(r)).toBe(10);
    expect(previewNotes(r).join(' ')).toMatch(/5 books have not been in the trash long enough yet/);
    expect(previewNotes({ ...r, not_yet_due: 1 }).join(' ')).toMatch(
      /1 book has not been in the trash long enough yet, so it is left alone/
    );
    expect(previewNotes({ ...r, not_yet_due: 0 })).toEqual([]);
  });
  it('books the preview showed as kept are decided and named in a finished pass', () => {
    const r: PurgeResult = {
      ...base,
      attempted: 6,
      purged: 3,
      skipped_owns_files: 1,
      not_in_preview: 2,
    };
    expect(decidedCount(r)).toBe(6);
    expect(outcomeNotes(r).join(' ')).toMatch(
      /2 books were shown as kept in the preview you confirmed/
    );
    expect(outcomeNotes({ ...r, not_in_preview: 1 }).join(' ')).toMatch(
      /1 book was shown as kept in the preview you confirmed and has since become purgeable/
    );
  });
  it('a finished pass names the window too', () => {
    const r: PurgeResult = { ...base, attempted: 4, purged: 3, not_yet_due: 1 };
    expect(decidedCount(r)).toBe(4);
    expect(outcomeNotes(r).join(' ')).toMatch(/not been in the trash long enough/);
  });
});

describe('previewFiles', () => {
  it('is null when the server sent no file list', () => {
    expect(previewFiles(base)).toBeNull();
  });
  it('caps the paths and counts the rest from the exact total', () => {
    const files = Array.from({ length: 4 }, (_, i) => `/lib/${i}.m4b`);
    const f = previewFiles(
      { ...base, files_to_delete: files, files_to_delete_count: 9, files_kept: 2 },
      3
    );
    expect(f).toEqual({ count: 9, kept: 2, shown: files.slice(0, 3), more: 6, earlier: 0 });
  });
});

describe('files an earlier purge left', () => {
  it('a preview reports how many are waiting, from the pending count', () => {
    const f = previewFiles({
      ...base,
      files_to_delete: [],
      files_to_delete_count: 0,
      files_pending_from_earlier_pass: 4,
    });
    expect(f?.earlier).toBe(4);
    expect(previewFiles({ ...base, files_to_delete_count: 0 })?.earlier).toBe(0);
  });
  it('a finished purge names the replayed files apart from its own count', () => {
    const r: PurgeResult = {
      ...base,
      attempted: 5,
      purged: 0,
      files_deleted: 0,
      files_replayed: 3,
    };
    // The headline counts this purge's books only: it removed none.
    expect(outcomeHeadline(r)).toBe('Purged none of the 5 books in the trash.');
    const notes = outcomeNotes(r).join(' ');
    expect(notes).toMatch(/3 files of books an earlier purge removed were also deleted from disk/);
    expect(notes).toMatch(/not in the count above/);
    expect(outcomeNotes({ ...r, files_replayed: 1 }).join(' ')).toMatch(
      /1 file of books an earlier purge removed was also deleted/
    );
    expect(outcomeNotes({ ...r, files_replayed: 0 })).toEqual([]);
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
