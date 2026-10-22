// file: web/src/services/api.combineBooks.test.ts
// version: 1.0.0
// guid: d6f20a83-9c15-4e7b-a834-5b1e9c7d0f26
// last-edited: 2026-10-22

// combineBooks against a stubbed fetch: POST /audiobooks/combine answers 202
// with the queued operation, not the finished result, and the refusals made
// before queueing (400, 404, 409) are ApiErrors carrying the server's reason.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, combineBooks } from './api';

const respond = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

describe('combineBooks', () => {
  beforeEach(() => vi.restoreAllMocks());

  it('returns the queued operation descriptor from the 202', async () => {
    const fetchMock = vi.fn(async () =>
      respond(202, {
        data: { id: 'op-c', type: 'book-combine', status: 'queued', detail: 'queued' },
      })
    );
    global.fetch = fetchMock as unknown as typeof fetch;
    const started = await combineBooks('book-a', ['book-b'], { title: 'T' });
    expect(started).toEqual({
      id: 'op-c',
      type: 'book-combine',
      status: 'queued',
      detail: 'queued',
    });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toContain('/audiobooks/combine');
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toEqual({
      keep_id: 'book-a',
      merge_ids: ['book-b'],
      override: { title: 'T' },
    });
  });

  it.each([
    [400, 'merge_ids must not be empty'],
    [404, 'book book-b not found'],
    [409, 'book book-b is in the trash'],
  ])('a %i refusal rejects with the server reason', async (status, message) => {
    global.fetch = vi.fn(async () =>
      respond(status, { error: message })
    ) as unknown as typeof fetch;
    const err = await combineBooks('book-a', ['book-b']).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(status);
    expect((err as ApiError).message).toContain(message);
  });
});
