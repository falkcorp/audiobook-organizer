// file: web/src/utils/deleteBookError.test.ts
// version: 1.4.0
// guid: 2d5f9315-d0df-4666-81b5-9b5423eb3f77
// last-edited: 2026-10-25

import { describe, expect, it } from 'vitest';
import { ApiError } from '../services/api';
import {
  describeDeleteBookError,
  isHasProgressRefusal,
  isOwnsFilesRefusal,
  isProgressDiscardedBookKeptRefusal,
  isStateMovedBookKeptRefusal,
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

  it('says the progress moved for STATE_MOVED_BOOK_KEPT, even though its text wraps the owns-files text', () => {
    const serverMsg =
      'purge b1: listening state moved to b2, but book b1 was not purged: book still owns book_file rows (1 row(s)); move them first';
    const err = new ApiError(serverMsg, 409, { code: 'STATE_MOVED_BOOK_KEPT' });
    const msg = describeDeleteBookError(err, 'f');
    expect(msg).toContain('listening progress on it had already been moved to another copy');
    expect(msg).toContain('moved to b2');
    expect(msg).not.toContain("can't be permanently deleted");
    expect(msg).not.toContain('Soft-delete it instead');
  });

  it('says the progress moved for a 500 STATE_MOVED_BOOK_KEPT too', () => {
    const err = new ApiError('hard delete b1: store failed', 500, {
      code: 'STATE_MOVED_BOOK_KEPT',
    });
    const msg = describeDeleteBookError(err, 'f');
    expect(msg).toContain('already been moved to another copy');
    expect(msg).toContain('store failed');
  });

  it("shows the server's wording for a purge or discard refusal of a trashed book, never advice to soft-delete it", () => {
    for (const action of ['purge', 'discard progress and purge']) {
      const m = `${action} b1: book still owns book_file rows (2 row(s)); move its files to another book first`;
      const err = new ApiError(m, 409, { code: 'OWNS_FILES' });
      expect(describeDeleteBookError(err, 'f', { inTrash: true })).toBe(m);
      // Without the option (a live-book delete) the canned sentence stays.
      expect(describeDeleteBookError(err, 'f')).toContain('Soft-delete it instead');
    }
  });

  it('says the progress was erased for PROGRESS_DISCARDED_BOOK_KEPT, even though its text wraps the owns-files text', () => {
    const serverMsg =
      'the listening progress of 1 user(s) and 0 bookmark(s) on b1 was erased and the book was kept (the delete failed: delete book b1: book still owns book_file rows (1 row(s)))';
    for (const status of [409, 500]) {
      const err = new ApiError(serverMsg, status, { code: 'PROGRESS_DISCARDED_BOOK_KEPT' });
      const msg = describeDeleteBookError(err, 'f', { inTrash: true });
      expect(msg).toContain('listening progress on it had already been erased');
      expect(msg).toContain(serverMsg);
      expect(msg).not.toContain('Soft-delete it instead');
      expect(isProgressDiscardedBookKeptRefusal(err)).toBe(true);
      expect(isOwnsFilesRefusal(err)).toBe(false);
    }
    expect(isProgressDiscardedBookKeptRefusal(new ApiError('x', 409, { code: 'OWNS_FILES' }))).toBe(
      false
    );
    expect(isProgressDiscardedBookKeptRefusal(new Error('x'))).toBe(false);
  });

  it('shows the server wording for retired rows instead of telling the user to move them', () => {
    const live =
      'hard delete b1: book still owns book_file rows (1 retired row(s) kept for another version of the book; they cannot be moved, so it cannot be deleted while they exist)';
    const mixed =
      'hard delete b1: book still owns book_file rows (2 live and 1 retired row(s)); its live files can be moved to another book first, but its retired rows cannot be moved, so it cannot be deleted while they exist';
    for (const m of [live, mixed]) {
      const err = new ApiError(m, 409, { code: 'OWNS_FILES' });
      expect(describeDeleteBookError(err, 'f')).toBe(m);
    }
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

  it('does not re-classify a response with another specific code by its text', () => {
    const err = new ApiError('x: book still owns book_file rows (2 row(s)); y', 409, {
      code: 'STATE_MOVED_BOOK_KEPT',
    });
    expect(isOwnsFilesRefusal(err)).toBe(false);
    expect(isStateMovedBookKeptRefusal(err)).toBe(true);
  });

  it('matches the text only when the response has no code', () => {
    expect(
      isOwnsFilesRefusal(new ApiError('x: book still owns book_file rows (2 row(s)); y', 409))
    ).toBe(true);
    expect(
      isOwnsFilesRefusal(new ApiError('x: book still owns book_file rows (2 row(s)); y', 409, {}))
    ).toBe(true);
  });

  it('is false for other refusals and errors', () => {
    expect(isOwnsFilesRefusal(new ApiError('x', 409, { code: 'HAS_PROGRESS' }))).toBe(false);
    expect(isOwnsFilesRefusal(new ApiError('x', 500, { code: 'OWNS_FILES' }))).toBe(false);
    expect(isOwnsFilesRefusal(new Error('book still owns book_file rows'))).toBe(false);
  });
});
