// file: web/src/services/api.awaitPurgeResult.test.ts
// version: 1.0.0
// guid: 8f3c1b57-4a92-4d06-b5e8-0c7a9d2e6f13
// last-edited: 2026-10-22

// awaitPurgeResult against a stubbed fetch: a purge that ended cancelled,
// failed or timed out still has a stored result (exact counts for the books
// decided so far) and it must be returned flagged stopped_early; when no
// result can be read the rejection must say the outcome is unknown and name
// the operation.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { awaitPurgeResult } from './api';

const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify({ data }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

function stub(status: string, resultResponse: () => Response, errorMessage?: string) {
  global.fetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith('/result')) return resultResponse();
    return json({ operation: { id: 'op-1', status, error_message: errorMessage } });
  }) as unknown as typeof fetch;
}

const stored = { attempted: 10, purged: 4, files_deleted: 0, errors: [] as string[] };

describe('awaitPurgeResult', () => {
  beforeEach(() => vi.restoreAllMocks());

  it('returns the stored result of a completed operation untouched', async () => {
    stub('completed', () => json({ result_data: stored }));
    const r = await awaitPurgeResult('op-1', undefined, { intervalMs: 0 });
    expect(r).toEqual(stored);
    expect(r.stopped_early).toBeUndefined();
  });

  it.each([
    ['canceled', undefined, 'cancelled'],
    ['failed', 'boom', 'boom'],
    ['interrupted_quiesced', undefined, 'interrupted_quiesced'],
  ])(
    'returns the stored result of a %s operation as stopped early',
    async (status, err, reason) => {
      stub(status, () => json({ result_data: stored }), err);
      const r = await awaitPurgeResult('op-1', undefined, { intervalMs: 0 });
      expect(r).toMatchObject({ ...stored, stopped_early: true, stop_reason: reason });
    }
  );

  it("keeps the server's own stop_reason", async () => {
    stub('failed', () => json({ result_data: { ...stored, stop_reason: 'timed out' } }), 'x');
    const r = await awaitPurgeResult('op-1', undefined, { intervalMs: 0 });
    expect(r.stop_reason).toBe('timed out');
  });

  it('says the outcome is unknown, naming the operation, when a stopped op has no result', async () => {
    stub('failed', () => json({ result_data: null }), 'worker died');
    await expect(awaitPurgeResult('op-1', undefined, { intervalMs: 0 })).rejects.toThrow(
      /ended as failed \(worker died\).*unknown.*operation op-1/s
    );
  });

  it('also treats an unreadable result (HTTP error) of a stopped op as unknown', async () => {
    stub('canceled', () => json({ error: 'not found' }, 404));
    await expect(awaitPurgeResult('op-1', undefined, { intervalMs: 0 })).rejects.toThrow(
      /unknown.*operation op-1/s
    );
  });

  it('rejects a completed operation that stored no result', async () => {
    stub('completed', () => json({ result_data: null }));
    await expect(awaitPurgeResult('op-1', undefined, { intervalMs: 0 })).rejects.toThrow(
      'stored no result'
    );
  });
});
