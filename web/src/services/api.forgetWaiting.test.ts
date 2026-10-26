// file: web/src/services/api.forgetWaiting.test.ts
// version: 1.0.0
// guid: 3b8e5d17-9c42-4a60-8f1d-7e2a0c6b4d93
// last-edited: 2026-10-26

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, forgetWaitingFileRecords, getSoftDeletedBooks } from './api';

const mockFetch = vi.fn();

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('forgetWaitingFileRecords', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('POSTs to the forget endpoint and returns what was forgotten', async () => {
    mockFetch.mockResolvedValueOnce(
      json({ data: { records_forgotten: 3, paths_forgotten: 2, warnings: ['not logged'] } })
    );
    const res = await forgetWaitingFileRecords();
    expect(res).toEqual({ records_forgotten: 3, paths_forgotten: 2, warnings: ['not logged'] });
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/audiobooks/purge-soft-deleted/forget-waiting-file-records');
    expect(init.method).toBe('POST');
  });

  it('surfaces a refusal as an ApiError (nothing was forgotten)', async () => {
    mockFetch.mockResolvedValueOnce(json({ error: 'no activity log' }, 503));
    const err = await forgetWaitingFileRecords().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(503);
  });
});

describe('getSoftDeletedBooks waiting-for-folder count', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('carries the waiting count apart from the pending count', async () => {
    mockFetch.mockResolvedValueOnce(
      json({
        data: {
          items: [],
          total: 0,
          files_pending_from_earlier_pass: 2,
          files_waiting_for_folder: 5,
        },
      })
    );
    const res = await getSoftDeletedBooks(1, 0);
    expect(res.pendingFileRemovals).toBe(2);
    expect(res.waitingFileRecords).toBe(5);
  });

  it('reads null (unknown) as null, never as zero', async () => {
    mockFetch.mockResolvedValueOnce(
      json({
        data: {
          items: [],
          total: 0,
          files_pending_from_earlier_pass: null,
          files_waiting_for_folder: null,
        },
      })
    );
    const res = await getSoftDeletedBooks(1, 0);
    expect(res.pendingFileRemovals).toBeNull();
    expect(res.waitingFileRecords).toBeNull();
  });
});
