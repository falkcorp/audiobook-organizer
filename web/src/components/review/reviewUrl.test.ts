// file: web/src/components/review/reviewUrl.test.ts
// version: 1.1.0
// guid: 8b3f6d12-5a90-4c47-b1e8-0d7a2e9c4f35
// last-edited: 2026-11-05

import { describe, expect, it } from 'vitest';
import { compareTabFromSlug, laneNamedBy, parseUrlId, readReviewUrl } from './reviewUrl';

const q = (s: string) => new URLSearchParams(s);

describe('laneNamedBy', () => {
  it('names the explicit lane, implies dupes from book or band, else nothing', () => {
    expect(laneNamedBy(q('lane=repairs'))).toBe('repairs');
    expect(laneNamedBy(q('lane=nope'))).toBeNull();
    expect(laneNamedBy(q('book=b1'))).toBe('dupes');
    expect(laneNamedBy(q('band=HIGH'))).toBe('dupes');
    expect(laneNamedBy(q('lane=metadata&band=HIGH'))).toBe('metadata');
    expect(laneNamedBy(q(''))).toBeNull();
  });
});

describe('readReviewUrl', () => {
  it("reads the dupes lane parameters and ignores another lane's", () => {
    const s = readReviewUrl(
      q('lane=dupes&view=labels&layer=embedding&compare=42&ctab=fingerprint&fixer=x')
    );
    expect(s.dupesView).toBe('labels');
    expect(s.layer).toBe('embedding');
    expect(s.compare).toBe(42);
    expect(s.compareTab).toBe(3);
    expect(s.repairs).toEqual({});
    expect(s.authorsView).toBe('authors');
  });

  it('reads `view` for the authors lane only when the URL is on it', () => {
    expect(readReviewUrl(q('lane=authors&view=series&author=7')).authorsView).toBe('series');
    expect(readReviewUrl(q('lane=authors&view=series&author=7')).authorId).toBe('7');
    expect(readReviewUrl(q('lane=dupes&view=series')).authorsView).toBe('authors');
    expect(readReviewUrl(q('lane=dupes&view=series')).dupesView).toBe('queue');
  });

  it('reads the repairs lane parameters and drops an unknown filter', () => {
    const s = readReviewUrl(
      q('lane=repairs&fixer=vg-primary&filter=skipped:no_candidate&class=robin')
    );
    expect(s.repairs).toEqual({
      fixerId: 'vg-primary',
      filter: 'skipped:no_candidate',
      rowClass: 'robin',
    });
    expect(readReviewUrl(q('lane=repairs&filter=junk')).repairs.filter).toBeNull();
  });

  it('holds a band to the known values and a class or filter suffix to a sane token', () => {
    expect(laneNamedBy(q('band=nonsense'))).toBeNull();
    expect(laneNamedBy(q('band=HIGH'))).toBe('dupes');
    const long = 'x'.repeat(65);
    for (const bad of ['has space', 'a/b', '%3Cscript%3E', long, '-lead', '']) {
      const r = readReviewUrl(q(`lane=repairs&class=${bad}&filter=skipped:${bad}`)).repairs;
      expect(r.rowClass, bad).toBeNull();
      expect(r.filter, bad).toBeNull();
    }
    expect(readReviewUrl(q('lane=repairs&filter=flag:needs-review.v2')).repairs.filter).toBe(
      'flag:needs-review.v2'
    );
  });

  it('falls back to defaults for junk', () => {
    const s = readReviewUrl(q('lane=dupes&layer=junk&compare=-3&ctab=junk&mode=junk'));
    expect(s.layer).toBeNull();
    expect(s.compare).toBeNull();
    expect(s.compareTab).toBe(0);
    expect(s.mode).toBe('compact');
  });
});

describe('ids and tabs', () => {
  it('parseUrlId takes positive integers only', () => {
    expect(parseUrlId('12')).toBe(12);
    expect(parseUrlId('0')).toBeNull();
    expect(parseUrlId('1.5')).toBeNull();
    expect(parseUrlId('x')).toBeNull();
  });
  it('compareTabFromSlug', () => {
    expect(compareTabFromSlug('metadata')).toBe(2);
    expect(compareTabFromSlug('nope')).toBe(0);
  });
});
