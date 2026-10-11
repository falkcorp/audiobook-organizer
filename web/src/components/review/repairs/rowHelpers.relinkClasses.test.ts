// file: web/src/components/review/repairs/rowHelpers.relinkClasses.test.ts
// version: 1.0.0
// guid: e3d7a2c9-14b8-4f65-a0d1-7c92b5e8f346
// last-edited: 2026-10-11

/**
 * The relink-pre-fix-imports trial lists a link row under the first evidence
 * check it fails, so the class chips show one count per check. Each of those
 * classes needs words: a chip reading "would_link_review_dates (12)" tells
 * the owner nothing.
 */
import { describe, expect, it } from 'vitest';
import { classLabel } from './rowHelpers';

describe('classLabel: relink-pre-fix-imports link classes', () => {
  const bases = ['would_link', 'same_book_version'];
  const checks = ['missing', 'location', 'format', 'dates'];
  const classes = bases.flatMap((b) => [b, ...checks.map((c) => `${b}_review_${c}`)]);

  it('has words for every link class (none falls back to the class id)', () => {
    for (const c of classes) {
      expect(classLabel(c), c).not.toBe(c);
    }
  });

  it('gives every class a distinct label, so no two chips read alike', () => {
    expect(new Set(classes.map(classLabel)).size).toBe(classes.length);
  });

  it('still falls back to the id for an unknown class', () => {
    expect(classLabel('no_such_class')).toBe('no_such_class');
  });
});
