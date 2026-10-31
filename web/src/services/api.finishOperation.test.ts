// file: web/src/services/api.finishOperation.test.ts
// version: 1.0.0
// guid: 91c4e7a2-5d38-4f0b-b6a9-3e82d1f75c04
// last-edited: 2026-10-30

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, finishOperation } from './api';

const mockFetch = vi.fn();

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('finishOperation', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('POSTs to the finish route of the operation and returns the queued row', async () => {
    mockFetch.mockResolvedValueOnce(
      json({
        data: {
          id: 'op-dark-knight',
          def_id: 'dedup.full-scan',
          status: 'queued',
          mode: 'finish_only',
        },
      })
    );
    const res = await finishOperation('op-dark-knight');
    expect(res).toEqual({
      id: 'op-dark-knight',
      def_id: 'dedup.full-scan',
      status: 'queued',
      mode: 'finish_only',
    });
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/operations/v2/op-dark-knight/finish');
    expect(init.method).toBe('POST');
  });

  it('escapes the id in the path', async () => {
    mockFetch.mockResolvedValueOnce(json({ data: { id: 'a/b' } }));
    await finishOperation('a/b');
    expect(String(mockFetch.mock.calls[0][0])).toContain('/operations/v2/a%2Fb/finish');
  });

  it('surfaces a refusal as an ApiError carrying the status', async () => {
    mockFetch.mockResolvedValueOnce(json({ error: 'operation op-x is running' }, 409));
    const err = await finishOperation('op-x').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
  });
});
