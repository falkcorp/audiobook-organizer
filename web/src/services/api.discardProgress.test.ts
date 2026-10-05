// file: web/src/services/api.discardProgress.test.ts
// version: 1.0.0
// guid: 612a5895-ef0c-4633-9676-8944c126c8cd
// last-edited: 2026-10-05

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, discardProgressAndPurge } from './api';

const mockFetch = vi.fn();

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('discardProgressAndPurge', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('POSTs to the per-book endpoint and returns the result', async () => {
    mockFetch.mockResolvedValueOnce(
      json({
        data: {
          book_id: 'b 1',
          title: 'T',
          users_cleared: 2,
          bookmarks_cleared: 1,
          files_deleted: 0,
        },
      })
    );
    const res = await discardProgressAndPurge('b 1');
    expect(res.users_cleared).toBe(2);
    const [url, init] = mockFetch.mock.calls[0];
    expect(String(url)).toContain('/audiobooks/b%201/discard-progress-and-purge');
    expect(init.method).toBe('POST');
  });

  it('surfaces the server refusal for a book not in the trash', async () => {
    mockFetch.mockResolvedValueOnce(json({ error: 'audiobook is not in the trash: b1' }, 409));
    const err = await discardProgressAndPurge('b1').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
    expect(err.message).toContain('not in the trash');
  });
});
