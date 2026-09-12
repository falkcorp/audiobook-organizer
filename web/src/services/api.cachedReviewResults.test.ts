// file: web/src/services/api.cachedReviewResults.test.ts
// version: 1.0.0
// guid: c3b7a114-67a4-4458-9425-4f872c5160a4
// last-edited: 2026-09-12
//
// getCachedReviewResults must send all=true when asked and ONLY when asked.
//
// The server caps a request with no positive limit to a default page unless it
// sends all=true. A client that dropped the flag would silently get the first
// page; one that always sent it would defeat the cap for every caller. Both are
// asserted on the URL that actually reaches fetch.

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

  it('sends all=true when the caller asks for the whole set', async () => {
    await getCachedReviewResults(0, 0, true);
    const params = requestedParams(fetchMock);
    expect(params.get('all')).toBe('true');
    expect(params.get('limit')).toBe('0');
    expect(params.get('offset')).toBe('0');
  });

  it('omits all by default so the server cap applies', async () => {
    await getCachedReviewResults(50, 100);
    const params = requestedParams(fetchMock);
    expect(params.has('all')).toBe(false);
    expect(params.get('limit')).toBe('50');
    expect(params.get('offset')).toBe('100');
  });

  it('carries truncated and limit through from the response', async () => {
    const data = await getCachedReviewResults(0, 0, true);
    expect(data.truncated).toBe(false);
    expect(data.limit).toBe(0);
  });
});
