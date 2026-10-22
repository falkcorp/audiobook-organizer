// file: web/src/utils/purgeSummary.test.ts
// version: 1.0.0
// guid: 5b2e8f13-9c47-4d60-a3e1-8f0d6b4a7c25
// last-edited: 2026-10-22

import { describe, expect, it } from 'vitest';
import type { PurgeResult } from '../services/api';
import {
  keptReasons,
  keptReasonsOfResult,
  outcomeHeadline,
  outcomeNotes,
  outcomeSeverity,
  previewHeadline,
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
  it('notes carried progress and failures', () => {
    const notes = outcomeNotes({ ...base, carried_to_sibling: 2, carry_failed: 1, failed: 3 });
    expect(notes).toHaveLength(3);
    expect(notes[0]).toContain('2 books had listening progress');
    expect(notes[1]).toContain('1 book; it was kept');
    expect(notes[2]).toContain('3 books could not be processed');
  });
});
