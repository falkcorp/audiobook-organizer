// file: web/src/pages/libraryCombineToast.test.ts
// version: 1.4.2
// guid: c0c7ee3f-0536-48da-951a-b0bfa6e93857
// last-edited: 2026-10-26

import { describe, it, expect } from 'vitest';
import { ApiError } from '../services/api';
import {
  describeCombineFailure,
  describeCombineResult,
  describeUndoFailure,
  describeUndoResult,
} from './libraryCombineToast';

describe('libraryCombineToast', () => {
  it('keeps both the journal-pending sentence and warnings', () => {
    const p = describeCombineResult({
      primary_id: 'a',
      files_moved: 3,
      books_deleted: 2,
      operation_id: 'op',
      journal_pending: 'j',
      warnings: ['W1.'],
    });
    expect(p.severity).toBe('warning');
    expect(p.message).toContain('moved 2 entries to trash');
    expect(p.message).toContain('could not be finalized');
    expect(p.message).toContain('W1.');
    expect(p.undoOperationId).toBe('op');
  });

  it('plain success without operation id has no undo', () => {
    const p = describeCombineResult({ primary_id: 'a', files_moved: 1, books_deleted: 1 });
    expect(p.severity).toBe('success');
    expect(p.undoOperationId).toBeUndefined();
  });

  it('failure without operation_id keeps the plain message', () => {
    expect(describeCombineFailure(new Error('x'))).toEqual({
      message: 'Failed to combine books.',
      severity: 'error',
    });
    const p = describeCombineFailure(new ApiError('boom', 500, { error: 'boom' }));
    expect(p.message).toBe('boom');
    expect(p.undoOperationId).toBeUndefined();
  });

  it('a retry-later refusal says nothing moved and to try again, with no undo', () => {
    const p = describeCombineFailure(
      new ApiError('the row of book Hush was not taken', 503, { code: 'COMBINE_RETRY_LATER' })
    );
    expect(p.severity).toBe('warning');
    expect(p.undoOperationId).toBeUndefined();
    expect(p.actionLabel).toBeUndefined();
    expect(p.message).toMatch(
      /^The combine was refused before it moved any files from the other books\./
    );
    expect(p.message).toContain('Try again');
    expect(p.message).toContain('was not taken');
  });

  it('a 503 without the retry-later code is still the plain failure', () => {
    const p = describeCombineFailure(new ApiError('down', 503, { code: 'OTHER' }));
    expect(p).toEqual({ message: 'down', severity: 'error' });
  });

  it('a pending journal without an operation id promises no undo', () => {
    const p = describeCombineResult({
      primary_id: 'a',
      files_moved: 1,
      books_deleted: 1,
      journal_pending: 'j',
    });
    expect(p.undoOperationId).toBeUndefined();
    expect(p.message).not.toMatch(/Undo|revert/);
  });

  it('an incomplete combine says what Undo does, without repeating the server text', () => {
    const p = describeCombineFailure(
      new ApiError(
        'The combine stopped part-way ... Revert operation op-1 to undo what it did.',
        500,
        {
          operation_id: 'op-1',
        }
      )
    );
    expect(p.undoOperationId).toBe('op-1');
    expect(p.actionLabel).toBe('Undo');
    expect(p.message.match(/stopped part-way/g)).toHaveLength(1);
    expect(p.message).toContain('operation op-1');
  });

  it('a partial undo offers Retry of the same operation', () => {
    const p = describeUndoResult({ message: 'm', partial: true }, 'op-2');
    expect(p.severity).toBe('warning');
    expect(p.undoOperationId).toBe('op-2');
    expect(p.actionLabel).toBe('Retry');
    expect(p.message).not.toContain('Activity');
    expect(p.message).toContain('operation op-2');
  });

  it('a partial undo shows the server message, combine notes included, and keeps Retry', () => {
    const message =
      'operation partially reverted: 1 of 3 changes restored; 1 combine(s) undone with some things left as they are: user u1 progress on survivor s1 changed after the merge; 1 failed to restore';
    const p = describeUndoResult({ message, partial: true, restored: 1, total: 3 }, 'op-5');
    expect(p.severity).toBe('warning');
    expect(p.message).toContain('(1 of 3 changes reverted)');
    expect(p.message).toContain(
      'user u1 progress on survivor s1 changed after the merge; 1 failed to restore.'
    );
    expect(p.undoOperationId).toBe('op-5');
    expect(p.actionLabel).toBe('Retry');
  });

  it('an undo that left things as they are is a warning that shows them, with no retry', () => {
    const message =
      'operation reverted: 2 of 2 changes restored; 1 combine(s) undone with some things left as they are: author 7 (narrator) was linked to book k after the combine; kept';
    const p = describeUndoResult(
      { message, partial: false, combines_undone_in_part: 1, kept: ['change c1: ...'] },
      'op-4'
    );
    expect(p.severity).toBe('warning');
    expect(p.message).toBe(message);
    expect(p.undoOperationId).toBeUndefined();
    expect(p.actionLabel).toBeUndefined();
  });

  it('a refused undo (409) offers no retry; a failed one does', () => {
    expect(
      describeUndoFailure(new ApiError('library changed', 409), 'op-3').undoOperationId
    ).toBeUndefined();
    const failed = describeUndoFailure(new ApiError('disk full', 500), 'op-3');
    expect(failed.undoOperationId).toBe('op-3');
    expect(failed.actionLabel).toBe('Retry');
    expect(failed.message).toContain('disk full');
    expect(failed.message).toContain('operation op-3');
  });
});
