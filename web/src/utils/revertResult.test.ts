// file: web/src/utils/revertResult.test.ts
// version: 1.0.0
// guid: 9b1f4c2e-6d3a-4e8b-a7f0-2c5d8e1b4a63
// last-edited: 2026-09-12

import { afterEach, describe, expect, it, vi } from 'vitest';
import { describeRevertResult } from './revertResult';
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
