// file: web/src/services/api.reviewItemsQuery.test.ts
// version: 1.0.0
// guid: 5e8c2f41-7b93-4d06-a1e5-3c7f0b9d6a28
// last-edited: 2026-09-01
//
// getReviewItems builds a URL. These tests assert on THAT URL.
//
// 🔴 WHY THIS FILE EXISTS AT ALL. useRegroupLane.test.ts mocks the whole api
// module and asserts with `lastFilter()`, which reads the ARGUMENT OBJECT the
// lane passed in. That proves the lane asked for a search; it proves nothing
// about whether the client turns that request into a query parameter, because
// the real function never runs. Measured: deleting the `q` line from api.ts
// entirely left all 43 lane tests passing. Two layers, one instrument -- the
// suite could not see the wire.
//
// So these drive the REAL getReviewItems against a stub fetch and read the URL
// it produced.

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import { getReviewItems } from './api';

/** A fetch that records its URL and answers with an empty, well-formed page. */
function capturingFetch() {
  return vi.fn(async (url: unknown) => {
    void url;
    return {
      ok: true,
      status: 200,
      json: async () => ({ items: [], count: 0, limit: 50, offset: 0, total: 0 }),
    } as unknown as Response;
  });
}

/** The query string of the single request made, parsed. */
function paramsOf(fetchMock: ReturnType<typeof capturingFetch>): URLSearchParams {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const url = String(fetchMock.mock.calls[0][0]);
  return new URLSearchParams(url.slice(url.indexOf('?') + 1));
}

describe('getReviewItems builds the review query string', () => {
  let fetchMock: ReturnType<typeof capturingFetch>;

  beforeEach(() => {
    fetchMock = capturingFetch();
    global.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('sends a search term as q', async () => {
    await getReviewItems({ search: 'mistborn' });
    expect(paramsOf(fetchMock).get('q')).toBe('mistborn');
  });

  it('trims the term rather than sending the padding', async () => {
    await getReviewItems({ search: '  mistborn  ' });
    expect(paramsOf(fetchMock).get('q')).toBe('mistborn');
  });

  it('omits q entirely for an empty term', async () => {
    await getReviewItems({ search: '' });
    // has(), not get(): `q=` present-but-empty is a filter for the empty string,
    // which is a different request from no filter at all.
    expect(paramsOf(fetchMock).has('q')).toBe(false);
  });

  it('omits q entirely for a whitespace-only term', async () => {
    // What a reviewer leaves behind when they select-all-and-space instead of
    // deleting. Without the trim this becomes a real filter matching nothing,
    // and the queue goes empty with no explanation on screen.
    await getReviewItems({ search: '   \t ' });
    expect(paramsOf(fetchMock).has('q')).toBe(false);
  });

  it('omits q when no search key is given', async () => {
    await getReviewItems({ kind: 'regroup.ambiguous' });
    expect(paramsOf(fetchMock).has('q')).toBe(false);
  });

  it('sends q alongside the other filters, not instead of them', async () => {
    await getReviewItems({ status: 'pending', kind: 'regroup.ambiguous', search: 'disc', limit: 500 });
    const p = paramsOf(fetchMock);
    expect(p.get('q')).toBe('disc');
    expect(p.get('kind')).toBe('regroup.ambiguous');
    expect(p.get('status')).toBe('pending');
    expect(p.get('limit')).toBe('500');
  });

  it('percent-encodes a term that would otherwise break the query string', async () => {
    // Folder names in this library contain spaces and ampersands; a raw
    // concatenation would silently truncate the term at the first &.
    await getReviewItems({ search: 'Fire & Blood' });
    // Read through URLSearchParams so this asserts the term SURVIVES the round
    // trip, not that it was encoded in one particular way.
    expect(paramsOf(fetchMock).get('q')).toBe('Fire & Blood');
  });
});
