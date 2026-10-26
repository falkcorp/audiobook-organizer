// file: web/src/services/api.forgetWaiting.test.ts
// version: 1.0.0
// guid: 3b8e5d17-9c42-4a60-8f1d-7e2a0c6b4d93
// last-edited: 2026-10-26

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  ApiError,
  cancelPurgePreview,
  forgetWaitingFileRecords,
  getSoftDeletedBooks,
  getWaitingFileRecordPaths,
} from './api';

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

  it('POSTs the listed paths to the forget endpoint and returns what was forgotten', async () => {
    mockFetch.mockResolvedValueOnce(
      json({ data: { records_forgotten: 3, paths_forgotten: 2, warnings: ['not logged'] } })
    );
    const paths = ['/lib/Gotham/Wayne/hush.m4b', '/lib/Gotham/Arkham/knightfall.m4b'];
    const res = await forgetWaitingFileRecords(paths);
    expect(res).toEqual({ records_forgotten: 3, paths_forgotten: 2, warnings: ['not logged'] });
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/audiobooks/purge-soft-deleted/forget-waiting-file-records');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ paths });
  });

  it('surfaces a refusal as an ApiError (nothing was forgotten)', async () => {
    mockFetch.mockResolvedValueOnce(json({ error: 'no activity log' }, 503));
    const err = await forgetWaitingFileRecords(['/lib/Gotham/Wayne/hush.m4b']).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(503);
  });
});

describe('getWaitingFileRecordPaths', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('GETs the listing the dialog shows before anything is forgotten', async () => {
    mockFetch.mockResolvedValueOnce(
      json({ data: { paths: ['/lib/Gotham/Wayne/hush.m4b'], total: 4, limit: 500 } })
    );
    const res = await getWaitingFileRecordPaths();
    expect(res).toEqual({ paths: ['/lib/Gotham/Wayne/hush.m4b'], total: 4, limit: 500 });
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/audiobooks/purge-soft-deleted/waiting-file-records');
    expect(init?.method ?? 'GET').toBe('GET');
  });

  it('surfaces a failure as an ApiError', async () => {
    mockFetch.mockResolvedValueOnce(json({ error: 'journal unreadable' }, 500));
    const err = await getWaitingFileRecordPaths().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(500);
  });
});

describe('cancelPurgePreview', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('DELETEs the purge preview route, not the settings.manage operations cancel', async () => {
    mockFetch.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await cancelPurgePreview('op-prev');
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/audiobooks/purge-soft-deleted/preview/op-prev');
    expect(String(url)).not.toContain('/operations/');
    expect(init.method).toBe('DELETE');
  });

  it("rejects with the 403 when the preview is not the caller's", async () => {
    mockFetch.mockResolvedValueOnce(json({ error: 'only the user who started this preview' }, 403));
    const err = await cancelPurgePreview('op-prev').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(403);
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
