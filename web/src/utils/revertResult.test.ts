// file: web/src/utils/revertResult.test.ts
// version: 1.1.0
// guid: 9b1f4c2e-6d3a-4e8b-a7f0-2c5d8e1b4a63
// last-edited: 2026-09-12

import { afterEach, describe, expect, it, vi } from 'vitest';
import { describeRevertResult, describeUndoPreflight } from './revertResult';
import { revertOperation } from '../services/versionApi';

describe('describeRevertResult', () => {
  it('shows the server summary for a partial revert, never a success line', () => {
    const msg = describeRevertResult({
      partial: true,
      message:
        'operation partially reverted: 1 of 2 changes restored; 1 cannot be undone automatically (1 author_delete row)',
      restored: 1,
      total: 2,
    });
    expect(msg).toMatch(/^Operation partially reverted/);
    expect(msg).toContain('author_delete');
    expect(msg).not.toMatch(/success/i);
  });

  it('builds a partial line from the counts when the message is missing', () => {
    const msg = describeRevertResult({ partial: true, restored: 0, total: 3 });
    expect(msg).toBe('Operation only partially reverted: 0 of 3 changes restored');
  });

  it('shows the server summary for a full revert', () => {
    expect(describeRevertResult({ partial: false, message: 'operation reverted: 2 of 2 changes restored' })).toBe(
      'Operation reverted: 2 of 2 changes restored',
    );
  });

  it('falls back when the server sent no body', () => {
    expect(describeRevertResult(undefined)).toBe('Operation reverted');
  });
});

describe('versionApi.revertOperation', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('returns the result unwrapped from the data envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ data: { message: 'operation partially reverted: x', partial: true, restored: 1 } }),
      }),
    );
    const result = await revertOperation('op-1');
    expect(result.partial).toBe(true);
    expect(result.restored).toBe(1);
  });

  it('throws the server message when every row is record-only (409)', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 409,
        statusText: 'Conflict',
        json: async () => ({
          error:
            "this operation's changes are a record only and cannot be undone automatically: 1742 author_delete rows",
        }),
      }),
    );
    await expect(revertOperation('op-1')).rejects.toThrow(/cannot be undone automatically/);
  });
});

describe('describeUndoPreflight', () => {
  const base = { total_changes: 0, already_reverted: 0, content_changed: [], book_deleted: [], re_organized: [], safe: 0 };

  it('does not offer Undo when no row can be restored', () => {
    const plan = describeUndoPreflight({
      ...base,
      total_changes: 1742,
      not_restorable: 1742,
      not_restorable_types: { author_delete: 1742 },
    });
    expect(plan.canUndo).toBe(false);
    expect(plan.message).toContain('1742 author_delete rows');
    expect(plan.message).not.toMatch(/Undo \d/);
  });

  it('offers only the restorable count and names the record-only rows', () => {
    const plan = describeUndoPreflight({
      ...base,
      total_changes: 3,
      safe: 1,
      not_restorable: 2,
      not_restorable_types: { author_delete: 1, 'metadata_update:author_id': 1 },
    });
    expect(plan.canUndo).toBe(true);
    expect(plan.message).toMatch(/^Undo 1 change\(s\)/);
    expect(plan.message).toContain('2 change(s) are a record only');
    expect(plan.message).toContain('1 metadata_update:author_id row');
  });

  it('counts conflicting rows among the rows the revert will attempt', () => {
    const plan = describeUndoPreflight({
      ...base,
      safe: 2,
      content_changed: [{ change_id: 'c', book_id: 'b', reason: 'content changed' }],
    });
    expect(plan.canUndo).toBe(true);
    expect(plan.message).toMatch(/^3 change\(s\) can be undone; 1 of them have conflicts/);
  });

  it('counts series_id rows whose old series was deleted as conflicts', () => {
    const plan = describeUndoPreflight({
      ...base,
      safe: 1,
      series_deleted: [{ change_id: 'c', book_id: 'b', reason: 'series deleted' }],
    });
    expect(plan.canUndo).toBe(true);
    expect(plan.message).toMatch(/^2 change\(s\) can be undone; 1 of them have conflicts/);
  });
});
