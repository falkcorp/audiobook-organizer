// file: web/src/services/api.cachedReviewResults.test.ts
// version: 1.2.0
// guid: c3b7a114-67a4-4458-9425-4f872c5160a4
// last-edited: 2026-10-19
//
// getCachedReviewResults is the lane's detail fetch: it sends the ids of the
// books it wants and nothing else. The listing's paging (all, limit, offset)
// and its bucket have no caller and are not parameters, so a request can never
// ask for the whole library or for a view the server retired. Asserted on the
// URL that actually reaches fetch.

import { vi, describe, it, expect, beforeEach } from 'vitest';
import { getCachedReviewResults } from './api';

function okFetch() {
  return vi.fn(
    async () =>
      new Response(
        JSON.stringify({
          data: { results: [], total_count: 0, matched: 0, no_match: 0, errors: 0, truncated: false, limit: 0 },
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      )
  );
}

function requestedParams(fetchMock: ReturnType<typeof okFetch>): URLSearchParams {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const url = String((fetchMock.mock.calls[0] as unknown[])[0]);
  expect(url).toContain('/api/v1/audiobooks/metadata/cache/review?');
  return new URL(url, 'http://localhost').searchParams;
}

describe('getCachedReviewResults query string', () => {
  let fetchMock: ReturnType<typeof okFetch>;

  beforeEach(() => {
    fetchMock = okFetch();
    global.fetch = fetchMock as unknown as typeof fetch;
  });

  it('sends the ids, comma-joined, and no listing parameter', async () => {
    await getCachedReviewResults(['b1', 'b2', 'b3']);
    const params = requestedParams(fetchMock);
    expect(params.get('ids')).toBe('b1,b2,b3');
    expect([...params.keys()]).toEqual(['ids']);
  });

  it('never sends a view, so the retired index view cannot be asked for', async () => {
    await getCachedReviewResults(['b1']);
    const params = requestedParams(fetchMock);
    expect(params.has('view')).toBe(false);
    expect(params.has('all')).toBe(false);
  });

  it('carries truncated and limit through from the response', async () => {
    const data = await getCachedReviewResults(['b1']);
    expect(data.truncated).toBe(false);
    expect(data.limit).toBe(0);
  });
});
