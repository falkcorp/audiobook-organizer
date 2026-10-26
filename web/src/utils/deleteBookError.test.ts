// file: web/src/utils/deleteBookError.test.ts
// version: 1.6.0
// guid: 2d5f9315-d0df-4666-81b5-9b5423eb3f77
// last-edited: 2026-10-26

import { describe, expect, it } from 'vitest';
import { ApiError } from '../services/api';
import {
  describeBlockOutcome,
  describeDeleteBookError,
  isHasProgressRefusal,
  isOwnsFilesRefusal,
  isProgressDiscardedBookKeptRefusal,
  isRootNotReadyRefusal,
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

  it('does not claim progress was erased when only reset markers were (records-only wording)', () => {
    const serverMsg =
      '1 stored listening record(s) on b1 (no progress, but reset markers or emptied state) were erased and the book was kept (the delete failed: boom)';
    const err = new ApiError(serverMsg, 500, { code: 'PROGRESS_DISCARDED_BOOK_KEPT' });
    const msg = describeDeleteBookError(err, 'f', { inTrash: true });
    expect(msg).not.toContain("every user's listening progress");
    expect(msg).toContain('had already been erased');
    expect(msg).toContain(serverMsg);
  });

  it('says the progress was erased for PROGRESS_DISCARDED_BOOK_KEPT, even though its text wraps the owns-files text', () => {
    const serverMsg =
      'the listening progress of 1 user(s) and 0 bookmark(s) on b1 was erased and the book was kept (the delete failed: delete book b1: book still owns book_file rows (1 row(s)))';
    for (const status of [409, 500]) {
      const err = new ApiError(serverMsg, status, { code: 'PROGRESS_DISCARDED_BOOK_KEPT' });
      const msg = describeDeleteBookError(err, 'f', { inTrash: true });
      expect(msg).toContain('listening state (progress, bookmarks or reset markers) had already been erased');
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

describe('isRootNotReadyRefusal', () => {
  it('recognises the 503 ROOT_NOT_READY refusal and explains it in plain words', () => {
    const err = new ApiError('purge b1: the library root is not ready: not mounted', 503, {
      code: 'ROOT_NOT_READY',
    });
    expect(isRootNotReadyRefusal(err)).toBe(true);
    const text = describeDeleteBookError(err, 'f');
    expect(text).toContain('Nothing was deleted');
    expect(text).toContain("isn't mounted");
    expect(text).toContain('the library root is not ready: not mounted');
  });

  it('is false for other 503s, other codes and plain errors', () => {
    expect(isRootNotReadyRefusal(new ApiError('x', 503, { code: 'SERVICE_UNAVAILABLE' }))).toBe(
      false
    );
    expect(isRootNotReadyRefusal(new ApiError('x', 500, { code: 'ROOT_NOT_READY' }))).toBe(false);
    expect(isRootNotReadyRefusal(new Error('x'))).toBe(false);
  });
});

describe('describeBlockOutcome', () => {
  it('says nothing when no block was asked for', () => {
    expect(describeBlockOutcome(false, { blocked: false })).toEqual({
      notice: '',
      severity: 'success',
    });
  });
  it('confirms a block that happened', () => {
    expect(describeBlockOutcome(true, { blocked: true })).toEqual({
      notice: ' Hash blocked.',
      severity: 'success',
    });
  });
  it('shows why a requested block was skipped (a live book holds the file)', () => {
    const r = describeBlockOutcome(true, {
      blocked: false,
      warnings: [
        "the file's hash was not blocked, because 1 other live book(s) hold the same file (b2)",
      ],
    });
    expect(r.severity).toBe('warning');
    expect(r.notice).toContain('b2');
    expect(r.notice).not.toContain('could not be blocked');
  });
  it('falls back to the generic text when the server gave no reason', () => {
    expect(describeBlockOutcome(true, { blocked: false })).toEqual({
      notice: ' Hash could not be blocked.',
      severity: 'warning',
    });
  });
});
