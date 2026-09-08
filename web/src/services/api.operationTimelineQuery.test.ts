// file: web/src/services/api.operationTimelineQuery.test.ts
// version: 1.0.0
// guid: 6b1f9d34-2c85-4a70-9e18-5d0a7c3f2b64
// last-edited: 2026-09-08
//
// getOperationTimeline builds a URL. These tests assert on THAT URL.
//
// 🔴 WHY THIS FILE EXISTS, and why the assertion in useOperationsStore.test.ts
// is not enough. That suite does `vi.mock('../services/api')`, so it can only
// check the ARGUMENT the store passed in — the real function never runs and the
// query string is never built. It would keep passing if `since` were dropped
// from the URL entirely. Same two-layers-one-instrument problem documented in
// api.reviewItemsQuery.test.ts, so the same remedy: drive the REAL function
// against a stub fetch and read the URL it produced.
//
// What is being pinned: the operations list read as EMPTY after every server
// restart because this asked for a 15-minute window. A restart drops the SSE
// stream, useOperationsStore's onError path calls loadFromServer, and that
// REPLACES the whole operations map with a quarter hour of history that a
// just-booted server has none of. Nothing was ever deleted — no code deletes
// `opv2:op:` rows at all — the view simply threw the records away.

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import {
  getOperationTimeline,
  OPERATION_TIMELINE_LIMIT,
  OPERATION_TIMELINE_WINDOW_MINUTES,
} from './api';

/** A fetch that records its URL and answers with a well-formed empty timeline. */
function capturingFetch(data: Record<string, unknown> = {}) {
  return vi.fn(async (url: unknown) => {
    void url;
    return {
      ok: true,
      status: 200,
      json: async () => ({
        data: { operations: [], matched: 0, limit: OPERATION_TIMELINE_LIMIT, ...data },
      }),
    } as unknown as Response;
  });
}

/** The query string of the single request made, parsed. */
function paramsOf(fetchMock: ReturnType<typeof capturingFetch>): URLSearchParams {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const url = String(fetchMock.mock.calls[0][0]);
  return new URLSearchParams(url.slice(url.indexOf('?') + 1));
}

describe('getOperationTimeline builds the timeline query string', () => {
  let fetchMock: ReturnType<typeof capturingFetch>;

  beforeEach(() => {
    fetchMock = capturingFetch();
    global.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('defaults to a 24-hour window', async () => {
    await getOperationTimeline();
    // 1440m, not 15m. This is the assertion the restart bug would have failed.
    expect(paramsOf(fetchMock).get('since')).toBe('1440m');
    expect(OPERATION_TIMELINE_WINDOW_MINUTES).toBe(24 * 60);
  });

  it('sends an explicit limit', async () => {
    await getOperationTimeline();
    // Without one the server applies its own default of 200, which is under a
    // busy day's volume — a 72h window on production matched 574 operations.
    // A day's history would be silently trimmed to its newest 200 entries.
    expect(paramsOf(fetchMock).get('limit')).toBe(String(OPERATION_TIMELINE_LIMIT));
  });

  it('honours an explicit window', async () => {
    await getOperationTimeline(60);
    expect(paramsOf(fetchMock).get('since')).toBe('60m');
  });

  it('warns when the server reports the result was truncated', async () => {
    // A short list that says nothing about being short is indistinguishable
    // from missing data — the failure this endpoint has already produced twice.
    fetchMock = capturingFetch({ truncated: true, matched: 5000 });
    global.fetch = fetchMock as unknown as typeof fetch;
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});

    await getOperationTimeline();

    expect(warn).toHaveBeenCalledTimes(1);
    expect(String(warn.mock.calls[0][0])).toContain('truncated');
  });

  it('does not warn on a complete result', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    await getOperationTimeline();
    expect(warn).not.toHaveBeenCalled();
  });

  it('returns the operations the server sent', async () => {
    fetchMock = capturingFetch({ operations: [{ id: 'op-1' }, { id: 'op-2' }] });
    global.fetch = fetchMock as unknown as typeof fetch;

    const ops = await getOperationTimeline();
    expect(ops.map((o) => o.id)).toEqual(['op-1', 'op-2']);
  });
});
