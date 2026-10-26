// file: web/src/utils/combineOutcome.test.ts
// version: 1.2.0
// guid: a2d94f60-8b17-4c3e-b5d9-1e7c0a6f3b85
// last-edited: 2026-10-22

import { describe, expect, it } from 'vitest';
import { describeCombineOutcome } from './combineOutcome';

const ok = { primary_id: 'book-a', files_moved: 3, books_deleted: 2 };

describe('describeCombineOutcome', () => {
  it('a plain success says how many files moved and books were trashed, with Undo on the result id', () => {
    const p = describeCombineOutcome({
      status: 'completed',
      result: { ...ok, operation_id: 'op-c' },
      operationId: 'op-c',
    });
    expect(p.severity).toBe('success');
    expect(p.message).toBe('Combined 3 files into one book; moved 2 entries to trash.');
    expect(p.undoOperationId).toBe('op-c');
    expect(p.actionLabel).toBe('Undo');
  });

  it('a success whose result has no operation_id says "undo id unavailable" and where to look, never just loses Undo', () => {
    const p = describeCombineOutcome({
      status: 'completed',
      result: ok,
      operationId: 'op-c',
    });
    expect(p.severity).toBe('warning');
    expect(p.message).toContain('Combined 3 files into one book');
    expect(p.message).toContain('Undo id unavailable');
    expect(p.message).toContain('Files & History');
    expect(p.message).toContain('operation op-c');
    expect(p.undoOperationId).toBeUndefined();
    expect(p.actionLabel).toBeUndefined();
  });

  it('offers Undo on the result operation id, and keeps journal-pending text and warnings', () => {
    const p = describeCombineOutcome({
      status: 'completed',
      result: { ...ok, operation_id: 'op-c', journal_pending: 'j-1', warnings: ['W1.'] },
      operationId: 'op-c',
    });
    expect(p.severity).toBe('warning');
    expect(p.message).toContain('could not be finalized');
    expect(p.message).toContain('W1.');
    expect(p.undoOperationId).toBe('op-c');
    expect(p.actionLabel).toBe('Undo');
  });

  it('a pending journal without an operation id promises no undo', () => {
    const p = describeCombineOutcome({
      status: 'completed',
      result: { ...ok, journal_pending: 'j-1' },
      operationId: 'op-c',
    });
    expect(p.undoOperationId).toBeUndefined();
    expect(p.message).not.toMatch(/Undo still puts/);
    expect(p.message).toContain('Undo id unavailable');
  });

  it('a failed combine that stopped part-way is an error naming the undo, never "Combined"', () => {
    const p = describeCombineOutcome({
      status: 'failed',
      errorMessage: 'move failed',
      result: {
        ...ok,
        files_moved: 1,
        failed: true,
        incomplete: true,
        operation_id: 'op-c',
        journal_id: 'j-2',
        error: 'The combine stopped part-way ... Revert operation op-c to undo what it did.',
      },
      operationId: 'op-c',
    });
    expect(p.severity).toBe('error');
    expect(p.message).not.toMatch(/Combined|Merged/);
    expect(p.message.match(/stopped part-way/g)).toHaveLength(1);
    expect(p.message).toContain('operation op-c');
    expect(p.undoOperationId).toBe('op-c');
    expect(p.actionLabel).toBe('Undo');
  });

  it('a part-way stop without an undo id points at the operation instead', () => {
    const p = describeCombineOutcome({
      status: 'failed',
      result: { ...ok, failed: true, incomplete: true },
      operationId: 'op-c',
    });
    expect(p.severity).toBe('error');
    expect(p.undoOperationId).toBeUndefined();
    expect(p.message).toContain('Undo id unavailable');
    expect(p.message).toContain('operation op-c');
  });

  it('a combine refused before any write says nothing was combined, with the reason', () => {
    const p = describeCombineOutcome({
      status: 'failed',
      result: {
        ...ok,
        files_moved: 0,
        books_deleted: 0,
        failed: true,
        error: 'book-b is protected',
      },
      operationId: 'op-c',
    });
    expect(p.severity).toBe('error');
    expect(p.message).toBe(
      'The combine was refused before changing anything: book-b is protected. Nothing was combined.'
    );
  });

  it('a failed op with no stored result never claims nothing was combined', () => {
    const p = describeCombineOutcome({
      status: 'failed',
      errorMessage: 'boom',
      result: null,
      operationId: 'op-c',
    });
    expect(p.severity).toBe('error');
    expect(p.message).toContain('did not complete (failed): boom');
    expect(p.message).toContain('Undo puts back whatever it changed');
    expect(p.message).not.toContain('Nothing was combined');
    // The undo journal is keyed by the combine's own operation id.
    expect(p.undoOperationId).toBe('op-c');
    expect(p.actionLabel).toBe('Undo');
    expect(p.message).not.toMatch(/^Combined/);
  });

  it('a cancelled op that stored a result without failed is not described as combined', () => {
    const p = describeCombineOutcome({ status: 'cancelled', result: ok, operationId: 'op-c' });
    expect(p.severity).toBe('error');
    expect(p.message).not.toMatch(/^Combined/);
  });

  it('a completed op with no readable result is unknown, not a success', () => {
    const p = describeCombineOutcome({ status: 'completed', result: null, operationId: 'op-c' });
    expect(p.severity).toBe('warning');
    expect(p.message).toContain('what it did is unknown');
    expect(p.message).toContain('operation op-c');
    expect(p.undoOperationId).toBe('op-c');
    expect(p.actionLabel).toBe('Undo');
  });
});
