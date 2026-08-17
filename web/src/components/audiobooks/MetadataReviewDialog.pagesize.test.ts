// file: web/src/components/audiobooks/MetadataReviewDialog.pagesize.test.ts
// version: 1.0.0
// guid: 4b1c9d2e-8f37-4a05-9c61-2d7e6a4b8f13
// last-edited: 2026-08-17

import { beforeEach, describe, expect, it } from 'vitest';

import { loadReviewPageSize } from './MetadataReviewDialog';
import { STORAGE_KEYS } from '../../lib/storageKeys';

const KEY = STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE;

/**
 * A stored page size of 250 froze the review dialog badly enough that the size
 * control — which lives inside the dialog — could not be reached to change it
 * back. Because the old loader merely checked membership in PAGE_SIZE_OPTIONS,
 * and 250 was a member, every reopen restored 250 and re-froze. The only escape
 * was clearing localStorage by hand.
 *
 * These tests pin the two properties that make it recoverable: an oversized
 * stored value is clamped on READ, and the correction is WRITTEN BACK so the
 * bad value is gone rather than re-clamped forever.
 */
describe('loadReviewPageSize', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it('clamps the 250 that caused the freeze down to 50', () => {
    window.localStorage.setItem(KEY, '250');
    expect(loadReviewPageSize()).toBe(50);
  });

  it('persists the correction, so the bad value cannot come back', () => {
    window.localStorage.setItem(KEY, '250');
    loadReviewPageSize();
    expect(window.localStorage.getItem(KEY)).toBe('50');

    // Second open reads the corrected value directly — not the clamp path.
    expect(loadReviewPageSize()).toBe(50);
  });

  it('clamps 500 as well, not just the reported 250', () => {
    window.localStorage.setItem(KEY, '500');
    expect(loadReviewPageSize()).toBe(50);
  });

  it.each([25, 50, 100])('leaves a still-offered size %i alone', (size) => {
    window.localStorage.setItem(KEY, String(size));
    expect(loadReviewPageSize()).toBe(size);
    // An in-range value must not be rewritten.
    expect(window.localStorage.getItem(KEY)).toBe(String(size));
  });

  it('falls back to 25 when nothing is stored', () => {
    expect(loadReviewPageSize()).toBe(25);
    // Absent must stay absent — do not seed a preference the user never set.
    expect(window.localStorage.getItem(KEY)).toBeNull();
  });

  it.each(['abc', '', '0', '-100', 'NaN'])(
    'falls back to 25 for the unusable stored value %j',
    (raw) => {
      window.localStorage.setItem(KEY, raw);
      expect(loadReviewPageSize()).toBe(25);
    }
  );
});
