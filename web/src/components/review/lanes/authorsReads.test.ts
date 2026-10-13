// file: web/src/components/review/lanes/authorsReads.test.ts
// version: 1.1.0
// guid: 91c4e7a0-3b58-4d2f-a6e1-80f5d2b9c347
// last-edited: 2026-10-13

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  AUTHORS_READ_TIMEOUT_MS,
  fetchAuthorGroups,
  fetchAuthorName,
  fetchBooksByAuthor,
  fetchSeriesGroups,
  validateAuthorName,
  validateSeriesName,
} from './authorsReads';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('fetchAuthorGroups', () => {
  it('unwraps the envelope and reports a cold cache as needsRefresh', async () => {
    fetchMock.mockResolvedValue(json({ data: { groups: null, needs_refresh: true } }));
    const ctl = new AbortController();
    await expect(fetchAuthorGroups(ctl.signal)).resolves.toEqual({ groups: [], needsRefresh: true });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/authors/duplicates');
  });

  it("aborting the caller's signal cancels the request in flight", async () => {
    fetchMock.mockImplementation(
      (_url: string, init: RequestInit) =>
        new Promise((_resolve, reject) => {
          init.signal?.addEventListener('abort', () =>
            reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
          );
        })
    );
    const ctl = new AbortController();
    const pending = fetchAuthorGroups(ctl.signal);
    ctl.abort();
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
  });

  it('a request that never answers ends in a timeout error, not an endless spinner', async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockImplementation(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            init.signal?.addEventListener('abort', () =>
              reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
            );
          })
      );
      const pending = fetchAuthorGroups(new AbortController().signal);
      const assertion = expect(pending).rejects.toMatchObject({ name: 'ApiTimeoutError' });
      await vi.advanceTimersByTimeAsync(AUTHORS_READ_TIMEOUT_MS + 1);
      await assertion;
    } finally {
      vi.useRealTimers();
    }
  });

  it('turns an error status into an Error carrying the server message', async () => {
    fetchMock.mockResolvedValue(json({ error: 'Internal server error' }, 500));
    await expect(fetchAuthorGroups(new AbortController().signal)).rejects.toThrow('Internal server error');
  });

  it('falls back to the status when the body has no message', async () => {
    fetchMock.mockResolvedValue(new Response('<html>bad gateway</html>', { status: 502 }));
    await expect(fetchAuthorGroups(new AbortController().signal)).rejects.toThrow(
      'Failed to fetch author duplicates (HTTP 502)'
    );
  });
});

describe('fetchBooksByAuthor', () => {
  it('walks every page until it holds the reported count', async () => {
    const book = (id: string) => ({ id, title: id });
    fetchMock
      .mockResolvedValueOnce(json({ data: { items: [book('a'), book('b')], count: 3 } }))
      .mockResolvedValueOnce(json({ data: { items: [book('c')], count: 3 } }));
    const books = await fetchBooksByAuthor(7, new AbortController().signal);
    expect(books.map((b) => b.id)).toEqual(['a', 'b', 'c']);
    expect(fetchMock.mock.calls[1][0]).toContain('offset=2');
    expect(fetchMock.mock.calls[0][0]).toContain('author_id=7');
  });

  it('accepts a body without the data envelope', async () => {
    fetchMock.mockResolvedValue(json({ items: [{ id: 'a', title: 'a' }], count: 1 }));
    await expect(fetchBooksByAuthor(7, new AbortController().signal)).resolves.toHaveLength(1);
  });

  it('throws on a failed later page rather than returning a partial list', async () => {
    fetchMock
      .mockResolvedValueOnce(json({ data: { items: [{ id: 'a', title: 'a' }], count: 2 } }))
      .mockResolvedValueOnce(json({ error: 'boom' }, 500));
    await expect(fetchBooksByAuthor(7, new AbortController().signal)).rejects.toThrow('boom');
  });
});

describe('validateAuthorName and fetchAuthorName', () => {
  it('posts the name as an author query', async () => {
    fetchMock.mockResolvedValue(json({ data: { results: [{ source: 'P', title: 'T', author: 'A' }], query: 'Author A' } }));
    const out = await validateAuthorName('Author A', new AbortController().signal);
    expect(out.results).toHaveLength(1);
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ query: 'Author A', type: 'author' });
  });

  it('reads an author name, and fails loudly when the author is gone', async () => {
    fetchMock.mockResolvedValueOnce(json({ data: { author: { id: 5, name: 'Author E' } } }));
    await expect(fetchAuthorName(5, new AbortController().signal)).resolves.toBe('Author E');
    fetchMock.mockResolvedValueOnce(json({ error: 'not found' }, 404));
    await expect(fetchAuthorName(6, new AbortController().signal)).rejects.toThrow('not found');
  });
});

describe('fetchSeriesGroups', () => {
  it('unwraps the envelope, reads the totals and reports a cold cache as needsRefresh', async () => {
    fetchMock.mockResolvedValue(json({ data: { groups: null, total_series: 41, needs_refresh: true } }));
    await expect(fetchSeriesGroups(new AbortController().signal)).resolves.toEqual({
      groups: [],
      totalSeries: 41,
      needsRefresh: true,
    });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/series/duplicates');
  });

  it('defaults a missing total to zero and a missing flag to false', async () => {
    fetchMock.mockResolvedValue(json({ data: { groups: [{ name: 'Series A', count: 2, series: [] }] } }));
    const out = await fetchSeriesGroups(new AbortController().signal);
    expect(out.totalSeries).toBe(0);
    expect(out.needsRefresh).toBe(false);
    expect(out.groups).toHaveLength(1);
  });

  it("aborting the caller's signal cancels the request in flight", async () => {
    fetchMock.mockImplementation(
      (_url: string, init: RequestInit) =>
        new Promise((_resolve, reject) => {
          init.signal?.addEventListener('abort', () =>
            reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
          );
        })
    );
    const ctl = new AbortController();
    const pending = fetchSeriesGroups(ctl.signal);
    ctl.abort();
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
  });

  it('a request that never answers ends in a timeout error, not an endless spinner', async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockImplementation(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            init.signal?.addEventListener('abort', () =>
              reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
            );
          })
      );
      const pending = fetchSeriesGroups(new AbortController().signal);
      const assertion = expect(pending).rejects.toMatchObject({ name: 'ApiTimeoutError' });
      await vi.advanceTimersByTimeAsync(AUTHORS_READ_TIMEOUT_MS + 1);
      await assertion;
    } finally {
      vi.useRealTimers();
    }
  });

  it('turns an error status into an Error carrying the server message', async () => {
    fetchMock.mockResolvedValue(json({ error: 'Internal server error' }, 500));
    await expect(fetchSeriesGroups(new AbortController().signal)).rejects.toThrow('Internal server error');
  });
});

describe('validateSeriesName', () => {
  it('posts the name as a series query', async () => {
    fetchMock.mockResolvedValue(json({ data: { results: [{ source: 'P', title: 'T', author: 'A' }], query: 'Series A' } }));
    const out = await validateSeriesName('Series A', new AbortController().signal);
    expect(out.results).toHaveLength(1);
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ query: 'Series A', type: 'series' });
  });

  it('fails with the server message when the providers are down', async () => {
    fetchMock.mockResolvedValue(json({ error: 'providers down' }, 502));
    await expect(validateSeriesName('Series A', new AbortController().signal)).rejects.toThrow('providers down');
  });
});
