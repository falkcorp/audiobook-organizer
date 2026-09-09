// file: web/src/services/api.cachedCandidatesQuery.test.ts
// version: 1.0.0
// guid: 4d81ca07-6f3b-4e29-95a7-1c0be8f2d534
// last-edited: 2026-09-09
//
// listCachedCandidates builds a URL. These tests assert on THAT URL, for the
// same reason api.operationTimelineQuery.test.ts does: a suite that mocks
// '../services/api' can only see the arguments a caller passed, never the query
// string the real function produced, so it would keep passing if `limit` were
// dropped from the URL entirely.
//
// What is being pinned: omitting `limit` is not a neutral default here. The
// server reads limit=0 as "return every row" (see the comment in
// internal/server/handlers/metadata_cache.go), which on production is 40,485
// rows in a 7.35 MB body — parsed in the browser on a button click, with every
// field then discarded, because the only caller needs a COUNT. `total` is the
// size of the filtered set no matter what `limit` is, so limit=1 returns the
// identical number. The regression this guards is silent: dropping the
// parameter changes no rendered output, only how much data crosses the wire.

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import { listCachedCandidates } from './api';

/** A fetch that records its URL and answers with a well-formed page. */
function capturingFetch(data: Record<string, unknown> = {}) {
  return vi.fn(async (url: unknown) => {
    void url;
    return {
      ok: true,
      status: 200,
      json: async () => ({ data: { entries: [], total: 0, ...data } }),
    } as unknown as Response;
  });
}

/** The URL of the single request made. */
function urlOf(fetchMock: ReturnType<typeof capturingFetch>): string {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  return String(fetchMock.mock.calls[0][0]);
}

/** The query string of the single request made, parsed. */
function paramsOf(fetchMock: ReturnType<typeof capturingFetch>): URLSearchParams {
  const url = urlOf(fetchMock);
  const q = url.indexOf('?');
  return new URLSearchParams(q === -1 ? '' : url.slice(q + 1));
}

describe('listCachedCandidates builds the cached-metadata query string', () => {
  let fetchMock: ReturnType<typeof capturingFetch>;

  beforeEach(() => {
    fetchMock = capturingFetch();
    global.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('sends the limit it was given alongside the status filter', async () => {
    await listCachedCandidates('pending', 1);
    const params = paramsOf(fetchMock);
    // Both, not either. An earlier version of this function built the query by
    // string concatenation off `status` alone, so there was nowhere for a limit
    // to go even if a caller passed one.
    expect(params.get('status')).toBe('pending');
    expect(params.get('limit')).toBe('1');
  });

  it('sends limit=0 when asked for zero explicitly', async () => {
    // 0 is meaningful to the server ("all rows"), so it must survive the
    // optional-parameter check. `if (limit)` would drop it and silently change
    // the request; `if (limit != null)` keeps it. Verified by mutation: swapping
    // in the truthiness check fails exactly this assertion.
    await listCachedCandidates('pending', 0);
    expect(paramsOf(fetchMock).get('limit')).toBe('0');
  });

  it('omits limit entirely when no limit is given', async () => {
    await listCachedCandidates('pending');
    expect(paramsOf(fetchMock).has('limit')).toBe(false);
  });

  it('sends no query string at all when given neither argument', async () => {
    await listCachedCandidates();
    expect(urlOf(fetchMock)).not.toContain('?');
  });

  it('reports total independently of how many rows came back', async () => {
    // The property the caller relies on: one row on the wire, the full filtered
    // count in `total`. If these were ever coupled, limit=1 would report "1
    // book ready for review" to a user with 40,485 of them.
    fetchMock = capturingFetch({ entries: [{ book_id: 'b1' }], total: 40485 });
    global.fetch = fetchMock as unknown as typeof fetch;

    const result = await listCachedCandidates('pending', 1);

    expect(result.total).toBe(40485);
    expect(result.entries).toHaveLength(1);
  });
});
