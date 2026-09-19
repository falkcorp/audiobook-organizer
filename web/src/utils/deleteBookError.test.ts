// file: web/src/utils/deleteBookError.test.ts
// version: 1.0.0
// guid: 2d5f9315-d0df-4666-81b5-9b5423eb3f77
// last-edited: 2026-09-19

import { describe, expect, it } from 'vitest';
import { ApiError } from '../services/api';
import { describeDeleteBookError } from './deleteBookError';

describe('describeDeleteBookError', () => {
  it('turns the 409 owns-files refusal into a next step with the row count', () => {
    const err = new ApiError(
      'hard delete b1: book still owns book_file rows (3 row(s)); soft-delete it instead, or move its files to another book first',
      409
    );
    const msg = describeDeleteBookError(err, 'Failed to purge audiobook.');
    expect(msg).toContain('still owns 3 file rows');
    expect(msg).toContain('Soft-delete it instead');
  });

  it('uses the singular for one row', () => {
    const err = new ApiError('x: book still owns book_file rows (1 row(s)); y', 409);
    expect(describeDeleteBookError(err, 'f')).toContain('still owns 1 file row,');
  });

  it("shows the server's message for other API errors", () => {
    const err = new ApiError('audiobook is already soft deleted', 409);
    expect(describeDeleteBookError(err, 'fallback')).toBe('audiobook is already soft deleted');
  });

  it('falls back for non-API errors', () => {
    expect(describeDeleteBookError(new Error('network'), 'fallback')).toBe('fallback');
  });
});
