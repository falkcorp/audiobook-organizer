// file: web/src/services/filterChangedOf.test.ts
// version: 1.0.0
// guid: 2b7e4c19-5d83-4a6f-9e02-8c1f3a6d7b45
// last-edited: 2026-10-06

import { describe, expect, it } from 'vitest';
import { ApiError, filterChangedOf } from './api';

describe('filterChangedOf', () => {
  it('reads the counts from a 409 FILTER_CHANGED', () => {
    const err = new ApiError('moved', 409, { code: 'FILTER_CHANGED', expected_total: 5, matched: 8 });
    expect(filterChangedOf(err)).toEqual({ expected: 5, matched: 8 });
  });

  it('ignores any other error', () => {
    expect(filterChangedOf(new ApiError('x', 409, { code: 'OTHER', matched: 1 }))).toBeNull();
    expect(filterChangedOf(new ApiError('x', 400, { code: 'FILTER_CHANGED', matched: 1 }))).toBeNull();
    expect(filterChangedOf(new Error('x'))).toBeNull();
  });
});
