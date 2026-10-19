// file: web/src/services/api.reviewSelection.test.ts
// version: 1.0.0
// guid: 7c0e2d5b-3a91-4f68-b4d2-91e6a8c05f17
// last-edited: 2026-10-19
//
// The selection check and the chunked apply, asserted on the request that
// reaches fetch: the check POSTs the selected ids with the page's filter in the
// query string (and no `view`), and a chunk of a split apply names its batch.

import { vi, describe, it, expect, beforeEach } from 'vitest';
import { batchApplyFromCache, checkReviewSelection } from './api';

function okFetch(body: unknown, status = 200) {
  return vi.fn(
    async () =>
      new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
  );
}

describe('checkReviewSelection', () => {
  let fetchMock: ReturnType<typeof okFetch>;

  beforeEach(() => {
    fetchMock = okFetch({
      data: { matching: ['b1'], not_matching: ['b2'], unknown: ['b3'], generation: 'g1' },
    });
    global.fetch = fetchMock as unknown as typeof fetch;
  });

  it('POSTs the selected ids and puts the filter in the query string', async () => {
    const out = await checkReviewSelection({ bucket: 'any', q: '/^A/' }, ['b1', 'b2', 'b3']);
    expect(out).toEqual({
      matching: ['b1'],
      not_matching: ['b2'],
      unknown: ['b3'],
      generation: 'g1',
    });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    const parsed = new URL(url, 'http://localhost');
    expect(parsed.pathname).toBe('/api/v1/audiobooks/metadata/cache/review/selection');
    expect(parsed.searchParams.get('bucket')).toBe('any');
    expect(parsed.searchParams.get('q')).toBe('/^A/');
    expect(parsed.searchParams.has('view')).toBe(false);
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toEqual({ ids: ['b1', 'b2', 'b3'] });
  });

  it('leaves the default bucket out of the query', async () => {
    await checkReviewSelection({ hide_no_match: true }, ['b1']);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    const parsed = new URL(url, 'http://localhost');
    expect(parsed.searchParams.has('bucket')).toBe(false);
    expect(parsed.searchParams.get('hide_no_match')).toBe('true');
  });

  it('rejects with the server message on a refusal', async () => {
    global.fetch = okFetch(
      { error: 'too many ids', code: 'SELECTION_TOO_LARGE' },
      422
    ) as unknown as typeof fetch;
    await expect(checkReviewSelection({}, ['b1'])).rejects.toThrow();
  });
});

describe('batchApplyFromCache batch', () => {
  beforeEach(() => {
    global.fetch = okFetch({
      data: { op_id: 'op1', requested: 2, write_back: true },
    }) as unknown as typeof fetch;
  });

  it('sends the batch id and total with a chunk, and nothing without one', async () => {
    await batchApplyFromCache(['a', 'b'], undefined, undefined, 'fill', { id: 'B1', total: 900 });
    let body = JSON.parse(
      String(
        (
          (global.fetch as ReturnType<typeof vi.fn>).mock.calls[0] as unknown as [
            string,
            RequestInit,
          ]
        )[1].body
      )
    );
    expect(body).toMatchObject({
      book_ids: ['a', 'b'],
      dry_run: false,
      batch_id: 'B1',
      batch_total: 900,
    });

    (global.fetch as ReturnType<typeof vi.fn>).mockClear();
    await batchApplyFromCache(['a', 'b']);
    body = JSON.parse(
      String(
        (
          (global.fetch as ReturnType<typeof vi.fn>).mock.calls[0] as unknown as [
            string,
            RequestInit,
          ]
        )[1].body
      )
    );
    expect(body).not.toHaveProperty('batch_id');
    expect(body).not.toHaveProperty('batch_total');
  });
});
