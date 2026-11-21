// file: web/src/components/review/repairs/rowHelpers.missingFiles.test.ts
// version: 0.1.0
// guid: 4c8d1e57-2a93-4b6f-9d70-e5a1c3f82b64
// last-edited: 2026-11-20

/**
 * The Missing files fixer's row class is "no_candidate" for a book the scan
 * found no file for, or the scan's match type for a book it did. A chip
 * reading "original_hash (3)" tells the owner nothing, so each needs words and
 * no two may read alike.
 */
import { describe, expect, it } from 'vitest';
import { classLabel } from './rowHelpers';

describe('classLabel: reconcile.missing-files classes', () => {
  const classes = ['no_candidate', 'hash', 'original_hash', 'filename'];

  it('words every class (none falls back to the class id)', () => {
    for (const c of classes) expect(classLabel(c), c).not.toBe(c);
  });

  it('gives every class a distinct label', () => {
    expect(new Set(classes.map(classLabel)).size).toBe(classes.length);
  });
});
