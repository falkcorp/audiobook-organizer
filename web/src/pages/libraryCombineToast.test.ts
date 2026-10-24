// file: web/src/pages/libraryCombineToast.test.ts
// version: 1.5.0
// guid: c0c7ee3f-0536-48da-951a-b0bfa6e93857
// last-edited: 2026-10-22

import { describe, it, expect } from 'vitest';
import { ApiError } from '../services/api';
import {
  describeCombineFailure,
  describeUndoFailure,
  describeUndoResult,
} from './libraryCombineToast';

describe('libraryCombineToast', () => {
  it('a refusal from the combine request keeps the server reason, or a plain message', () => {
    expect(describeCombineFailure(new Error('x'))).toEqual({
      message: 'Failed to combine books.',
      severity: 'error',
    });
    const p = describeCombineFailure(new ApiError('book-b is in the trash', 409));
    expect(p.message).toBe('book-b is in the trash');
    expect(p.severity).toBe('error');
    expect(p.undoOperationId).toBeUndefined();
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
