// file: web/src/services/api.seriesRename.test.ts
// version: 1.2.0
// guid: 3c8f1e2a-7b54-4d19-9a6e-5f0c2d8b1a47
// last-edited: 2026-09-12

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renameSeries, SERIES_RENAME_WAIT_MS, undoSeriesRename, updateSeriesName } from './api';

// Both series rename endpoints answer 202 with a queued
// entities.series-rename operation id. The client polls
// GET /operations/v2/:id until the operation is terminal and resolves only
// when it completed.

const mockFetch = vi.fn();

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const queued = () =>
  json({ data: { id: 'op-1', type: 'entities.series-rename', status: 'queued' } }, 202);

const opStatus = (status: string, error_message?: string) =>
  json({
    data: {
      operation: {
        id: 'op-1',
        def_id: 'entities.series-rename',
        status,
        queued_at: '2026-09-12T00:00:00Z',
        error_message,
      },
    },
  });

describe('series rename callers', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('renameSeries PUTs the name, polls the queued op and resolves when it completed', async () => {
    mockFetch.mockResolvedValueOnce(queued()).mockResolvedValueOnce(opStatus('completed'));
    const op = await renameSeries(5, 'New');
    expect(op.id).toBe('op-1');
    expect(op.status).toBe('completed');
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/series/5/name');
    expect(init.method).toBe('PUT');
    expect(JSON.parse(init.body)).toEqual({ name: 'New' });
    expect(String(mockFetch.mock.calls[1][0])).toContain('/operations/v2/op-1');
  });

  it('updateSeriesName PATCHes the name and waits for the op', async () => {
    mockFetch.mockResolvedValueOnce(queued()).mockResolvedValueOnce(opStatus('completed'));
    const op = await updateSeriesName(7, 'Renamed');
    expect(op.status).toBe('completed');
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/series/7');
    expect(init.method).toBe('PATCH');
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it('rejects with the operation error when the op failed', async () => {
    mockFetch
      .mockResolvedValueOnce(queued())
      .mockResolvedValueOnce(opStatus('failed', 'series-rename: series 5 not found'));
    await expect(renameSeries(5, 'New')).rejects.toThrow('series-rename: series 5 not found');
  });

  it('stops waiting with "rename still queued" when the op never finishes', async () => {
    vi.useFakeTimers();
    try {
      mockFetch
        .mockResolvedValueOnce(queued())
        .mockImplementation(() => Promise.resolve(opStatus('queued')));
      const settled = expect(renameSeries(5, 'New')).rejects.toThrow(
        'rename still queued (op op-1)'
      );
      await vi.advanceTimersByTimeAsync(SERIES_RENAME_WAIT_MS + 2000);
      await settled;
      // It polled while waiting, then gave up instead of looping forever.
      const polls = mockFetch.mock.calls.length - 1;
      expect(polls).toBeGreaterThan(1);
      const after = mockFetch.mock.calls.length;
      await vi.advanceTimersByTimeAsync(10_000);
      expect(mockFetch.mock.calls.length).toBe(after);
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not time out an op that completes before the deadline', async () => {
    vi.useFakeTimers();
    try {
      mockFetch
        .mockResolvedValueOnce(queued())
        .mockResolvedValueOnce(opStatus('running'))
        .mockResolvedValueOnce(opStatus('completed'));
      const done = updateSeriesName(5, 'New');
      await vi.advanceTimersByTimeAsync(1500);
      await expect(done).resolves.toMatchObject({ status: 'completed' });
    } finally {
      vi.useRealTimers();
    }
  });

  it('rejects when the op ended in a non-completed terminal state without an error', async () => {
    mockFetch.mockResolvedValueOnce(queued()).mockResolvedValueOnce(opStatus('canceled'));
    await expect(updateSeriesName(5, 'New')).rejects.toThrow(/operation canceled/);
  });

  it('surfaces a synchronous validation error without polling', async () => {
    mockFetch.mockResolvedValueOnce(json({ error: 'name must not be empty' }, 400));
    await expect(renameSeries(5, ' ')).rejects.toThrow();
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it('rejects when the 202 body carries no operation id', async () => {
    mockFetch.mockResolvedValueOnce(json({ data: {} }, 202));
    await expect(updateSeriesName(5, 'New')).rejects.toThrow(/no operation id/);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });
});

// Undo goes through the operation's journal (POST /operations/:id/revert)
// rather than issuing a second rename, and rejects when nothing was restored.
describe('undoSeriesRename', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('POSTs the revert of the rename operation and resolves when it restored', async () => {
    mockFetch.mockResolvedValueOnce(
      json({ data: { message: 'reverted', partial: false, total: 1, restored: 1, failed: 0 } })
    );
    const res = await undoSeriesRename('op-1');
    expect(res.restored).toBe(1);
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/operations/op-1/revert');
    expect(init.method).toBe('POST');
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it('rejects when the series was renamed since and the revert restored nothing', async () => {
    mockFetch.mockResolvedValueOnce(
      json({
        data: {
          message: 'operation partially reverted',
          partial: true,
          total: 1,
          restored: 0,
          failed: 1,
        },
      })
    );
    await expect(undoSeriesRename('op-1')).rejects.toThrow('operation partially reverted');
  });

  it('rejects when the server refuses the revert', async () => {
    mockFetch.mockResolvedValueOnce(json({ error: 'nothing to revert' }, 409));
    await expect(undoSeriesRename('op-1')).rejects.toThrow();
  });
});
