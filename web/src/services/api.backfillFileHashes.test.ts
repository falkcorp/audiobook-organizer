// file: web/src/services/api.backfillFileHashes.test.ts
// version: 1.0.0
// guid: 0b7d4e92-5a1c-4f38-9c6e-8d2a7f3b1e50
// last-edited: 2026-10-25

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { HASH_ONLY_BACKFILL_PARAMS, backfillFileHashes, runMaintenanceJob } from './api';

const mockFetch = vi.fn();

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('one-click hash backfill', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('backfillFileHashes asks for a hash-only run: absences are not recorded', async () => {
    mockFetch.mockResolvedValueOnce(json({ operation_id: 'op-1' }));
    await backfillFileHashes(false);
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/maintenance/jobs/backfill-file-hashes');
    expect(JSON.parse(init.body)).toEqual({ dry_run: false, mark_absent: false });
  });

  it('the Fingerprint button path (runMaintenanceJob with the shared params) sends the same', async () => {
    mockFetch.mockResolvedValueOnce(json({ data: { operation_id: 'op-2' } }));
    await runMaintenanceJob('backfill-file-hashes', false, { ...HASH_ONLY_BACKFILL_PARAMS });
    const [, init] = mockFetch.mock.calls[0];
    expect(JSON.parse(init.body)).toEqual({ dry_run: false, mark_absent: false });
  });

  it('the shared params never ask the job to record absences', () => {
    expect(HASH_ONLY_BACKFILL_PARAMS.mark_absent).toBe(false);
  });
});
