// file: web/src/components/review/spine/rowState.test.ts
// version: 1.1.0
// guid: a80c5f34-7d16-4e92-b503-6c1f9a27e408
// last-edited: 2026-08-20

import { describe, expect, it } from 'vitest';
import {
  RUNTIME_WARN_THRESHOLD_SEC,
  isDecided,
  formatDuration,
  formatFileSize,
  getRowSx,
  isRowActionable,
  runtimeDiffers,
  scoreColor,
  type RowState,
} from './rowState';

// These are lifted from MetadataReviewDialog rather than rewritten, and the two
// derivations below each contain an asymmetry that a rewrite would "correct".
// The tests exist to make correcting them a deliberate act.

describe('isRowActionable', () => {
  it('keeps a skipped row actionable', () => {
    // The one that matters. Skip means "not now" -- the reviewer must be able to
    // come back and apply it in the same session. The simplifying rewrite,
    // `state !== undefined`, compiles, reads better, and makes every skip
    // permanent with no error anywhere.
    expect(isRowActionable('skipped')).toBe(true);
  });

  it('closes a row that has been applied or rejected', () => {
    expect(isRowActionable('applied')).toBe(false);
    expect(isRowActionable('rejected')).toBe(false);
  });

  it('treats both undecided representations as actionable', () => {
    // 'pending' is explicit and `undefined` is absent; neither is a decision.
    expect(isRowActionable(undefined)).toBe(true);
    expect(isRowActionable('pending')).toBe(true);
  });
});

describe('isDecided', () => {
  it('does not count a seeded pending row as decided', () => {
    // The dialog seeds 'pending' for every row on fetch, so `state !== undefined`
    // -- the obvious test -- reports the entire page decided the moment it loads.
    expect(isDecided('pending')).toBe(false);
    expect(isDecided(undefined)).toBe(false);
  });

  it('counts the three real outcomes', () => {
    expect(isDecided('applied')).toBe(true);
    expect(isDecided('rejected')).toBe(true);
    expect(isDecided('skipped')).toBe(true);
  });
});

describe('getRowSx', () => {
  it('gives applied and skipped rows a background', () => {
    expect(getRowSx('applied')).toMatchObject({ bgcolor: 'success.main', opacity: 0.6 });
    expect(getRowSx('skipped')).toMatchObject({
      bgcolor: 'action.disabledBackground',
      opacity: 0.5,
    });
  });

  it('deliberately leaves a rejected row undimmed', () => {
    // Reads as a missing branch; is not one. A rejected row carries a
    // "Rejected -- click to undo" chip, and dimming the row would bury the undo
    // affordance. Change this because the treatment was reconsidered, not
    // because the branch looked absent.
    const rejected = getRowSx('rejected') as Record<string, unknown>;
    const undecided = getRowSx(undefined) as Record<string, unknown>;
    expect(rejected).toEqual(undecided);
    expect(rejected.bgcolor).toBeUndefined();
  });

  it('always returns the shared radius and transition', () => {
    const states: Array<RowState | undefined> = [
      'applied',
      'rejected',
      'skipped',
      'pending',
      undefined,
    ];
    for (const state of states) {
      expect(getRowSx(state)).toMatchObject({ borderRadius: 1, transition: 'all 0.3s' });
    }
  });
});

describe('formatDuration', () => {
  it('drops the hours segment under an hour', () => {
    expect(formatDuration(0)).toBe('0m');
    expect(formatDuration(59)).toBe('0m');
    expect(formatDuration(600)).toBe('10m');
  });

  it('shows hours and minutes above an hour', () => {
    expect(formatDuration(3600)).toBe('1h 0m');
    expect(formatDuration(12000)).toBe('3h 20m');
    expect(formatDuration(86400)).toBe('24h 0m');
  });
});

describe('formatFileSize', () => {
  it('switches units at binary boundaries', () => {
    expect(formatFileSize(1024)).toBe('1 KB');
    expect(formatFileSize(1048576)).toBe('1 MB');
    expect(formatFileSize(1073741824)).toBe('1.0 GB');
  });

  it('shows one decimal only for GB', () => {
    // Carried from the dialog: an audiobook is usually a few hundred MB, where a
    // decimal is noise, but GB-scale files are where the difference matters.
    expect(formatFileSize(5 * 1048576)).toBe('5 MB');
    expect(formatFileSize(Math.round(2.5 * 1073741824))).toBe('2.5 GB');
  });
});

describe('scoreColor', () => {
  it('bands at the dialog thresholds, inclusive', () => {
    expect(scoreColor(0.85)).toBe('success');
    expect(scoreColor(0.8499)).toBe('warning');
    expect(scoreColor(0.6)).toBe('warning');
    expect(scoreColor(0.5999)).toBe('default');
  });
});

describe('runtimeDiffers', () => {
  it('warns strictly above ten minutes', () => {
    expect(runtimeDiffers(RUNTIME_WARN_THRESHOLD_SEC)).toBe(false);
    expect(runtimeDiffers(RUNTIME_WARN_THRESHOLD_SEC + 1)).toBe(true);
  });

  it('is symmetric, because either side can be the longer one', () => {
    expect(runtimeDiffers(-900)).toBe(true);
    expect(runtimeDiffers(900)).toBe(true);
  });

  it('treats an unknown runtime as not-differing rather than as differing', () => {
    // A missing delta means we could not compare, not that the runtimes clash.
    // Warning here would put a scary chip on every candidate with no duration.
    expect(runtimeDiffers(undefined)).toBe(false);
    expect(runtimeDiffers(null)).toBe(false);
  });
});
