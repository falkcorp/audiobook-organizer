// file: web/src/utils/deleteBookError.test.ts
// version: 1.2.0
// guid: 2d5f9315-d0df-4666-81b5-9b5423eb3f77
// last-edited: 2026-10-06

import { describe, expect, it } from 'vitest';
import { ApiError } from '../services/api';
import {
  describeDeleteBookError,
  isHasProgressRefusal,
  isOwnsFilesRefusal,
} from './deleteBookError';

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

describe('isHasProgressRefusal', () => {
  it('recognises the 409 HAS_PROGRESS refusal by its code', () => {
    const err = new ApiError('purge b1: the book has listening progress ...', 409, {
      code: 'HAS_PROGRESS',
    });
    expect(isHasProgressRefusal(err)).toBe(true);
    expect(describeDeleteBookError(err, 'f')).toContain('listening progress');
  });

  it('is false for other refusals and errors', () => {
    expect(isHasProgressRefusal(new ApiError('x', 409, { code: 'CONFLICT' }))).toBe(false);
    expect(isHasProgressRefusal(new ApiError('x', 500, { code: 'HAS_PROGRESS' }))).toBe(false);
    expect(isHasProgressRefusal(new Error('x'))).toBe(false);
  });
});

describe('isOwnsFilesRefusal', () => {
  it('recognises the 409 OWNS_FILES refusal by its code, whatever the message', () => {
    const err = new ApiError(
      'purge b1: book still owns rows: the purge never deletes a book that owns file rows',
      409,
      { code: 'OWNS_FILES' }
    );
    expect(isOwnsFilesRefusal(err)).toBe(true);
    expect(describeDeleteBookError(err, 'f')).toContain('still owns file rows');
  });

  it('still recognises the message from a server without the code', () => {
    const err = new ApiError('x: book still owns book_file rows (2 row(s)); y', 409, {
      code: 'CONFLICT',
    });
    expect(isOwnsFilesRefusal(err)).toBe(true);
  });

  it('is false for other refusals and errors', () => {
    expect(isOwnsFilesRefusal(new ApiError('x', 409, { code: 'HAS_PROGRESS' }))).toBe(false);
    expect(isOwnsFilesRefusal(new ApiError('x', 500, { code: 'OWNS_FILES' }))).toBe(false);
    expect(isOwnsFilesRefusal(new Error('book still owns book_file rows'))).toBe(false);
  });
});
