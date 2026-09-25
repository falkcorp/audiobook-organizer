// file: web/src/services/api.searchPoll.test.ts
// version: 1.1.0
// guid: 51875e2c-533f-4b08-beaf-f758cf4d4b54
// last-edited: 2026-09-25

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getBooks, searchBooks, SEARCH_PAGE_SIZE, SEARCH_POLL_INTERVAL_MS } from './api';

// A list search the server cannot finish within its wait answers
// 202 {search_id, status, matches_so_far}; the client polls
// GET /search/:search_id until done and re-issues the original request.
// The quick search walks every page instead of stopping at one.

const mockFetch = vi.fn();

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const book = (id: string) => ({ id, title: `Book ${id}` });

describe('list search 202 polling', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
    vi.useFakeTimers();
  });
  afterEach(() => {
    mockFetch.mockReset();
    vi.useRealTimers();
  });

  it('polls a pending search until done, then re-issues the request', async () => {
    mockFetch
      .mockResolvedValueOnce(json({ search_id: 's1', status: 'running', matches_so_far: 3 }, 202))
      .mockResolvedValueOnce(json({ data: { search_id: 's1', status: 'running', matches_so_far: 7 } }))
      .mockResolvedValueOnce(json({ data: { search_id: 's1', status: 'done', matches_so_far: 9 } }))
      .mockResolvedValueOnce(json({ data: { items: [book('a'), book('b')], count: 9 } }));

    const progress: number[] = [];
    const p = getBooks(2, 0, { search: 'odyssey', onSearchPending: (n) => progress.push(n) });
    await vi.advanceTimersByTimeAsync(SEARCH_POLL_INTERVAL_MS * 3);
    const page = await p;

    expect(page.count).toBe(9);
    expect(page.items.map((b) => b.id)).toEqual(['a', 'b']);
    expect(progress).toEqual([3, 7, 9]);
    const urls = mockFetch.mock.calls.map((c) => String(c[0]));
    expect(urls[1]).toContain('/search/s1');
    expect(urls[2]).toContain('/search/s1');
    expect(urls[3]).toBe(urls[0]);
  });

  it('surfaces a failed search instead of polling forever', async () => {
    mockFetch
      .mockResolvedValueOnce(json({ search_id: 's2', status: 'running', matches_so_far: 0 }, 202))
      .mockResolvedValueOnce(json({ data: { search_id: 's2', status: 'error', error: 'boom' } }));
    const p = getBooks(10, 0, { search: 'x' });
    const assertion = expect(p).rejects.toThrow('boom');
    await vi.advanceTimersByTimeAsync(SEARCH_POLL_INTERVAL_MS);
    await assertion;
  });

  it('reports stale results', async () => {
    mockFetch.mockResolvedValueOnce(json({ data: { items: [], count: 0, stale: true } }));
    const page = await getBooks(10, 0, { search: 'x' });
    expect(page.stale).toBe(true);
  });
});

describe('quick search paging', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => {
    mockFetch.mockReset();
  });

  it('walks every page instead of stopping at the first', async () => {
    const total = SEARCH_PAGE_SIZE * 2 + 7;
    const all = Array.from({ length: total }, (_, i) => book(`b${i}`));
    mockFetch.mockImplementation(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://x');
      const limit = Number(url.searchParams.get('limit'));
      const offset = Number(url.searchParams.get('offset'));
      return json({ data: { items: all.slice(offset, offset + limit), count: total } });
    });

    const got = await searchBooks('odyssey');
    expect(got.map((b) => b.id)).toEqual(all.map((b) => b.id));
    expect(mockFetch).toHaveBeenCalledTimes(3);
    const offsets = mockFetch.mock.calls.map((c) => new URL(String(c[0]), 'http://x').searchParams.get('offset'));
    expect(offsets).toEqual(['0', String(SEARCH_PAGE_SIZE), String(SEARCH_PAGE_SIZE * 2)]);
  });

  it('honours a result cap with one short request', async () => {
    const ten = Array.from({ length: 10 }, (_, i) => book(`c${i}`));
    mockFetch.mockResolvedValueOnce(json({ data: { items: ten, count: 500 } }));
    const got = await searchBooks('odyssey', 10);
    expect(got).toHaveLength(10);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(String(mockFetch.mock.calls[0][0])).toContain('limit=10');
  });

  it('stops on an empty page even if the count is larger', async () => {
    mockFetch.mockResolvedValueOnce(json({ data: { items: [], count: 99 } }));
    const got = await searchBooks('odyssey');
    expect(got).toEqual([]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  // Review finding 24: the list can change between two page fetches. A walk
  // never keeps a book twice, and restarts when the count changes mid-walk.
  it('restarts a walk whose count changed and never returns a book twice', async () => {
    const before = Array.from({ length: SEARCH_PAGE_SIZE * 2 }, (_, i) => book(`b${String(i).padStart(3, '0')}`));
    // One book inserted at the front after the first page was served: page 2
    // would start with page 1's last book.
    const after = [book('new'), ...before];
    let calls = 0;
    mockFetch.mockImplementation(async (input: RequestInfo | URL) => {
      calls++;
      const url = new URL(String(input), 'http://x');
      const limit = Number(url.searchParams.get('limit'));
      const offset = Number(url.searchParams.get('offset'));
      const list = calls === 1 ? before : after;
      return json({ data: { items: list.slice(offset, offset + limit), count: list.length } });
    });
    const got = await searchBooks('odyssey');
    const ids = got.map((b) => b.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids).toEqual(after.map((b) => b.id));
  });

  // Review finding 18: the 202 path is opt-in per request.
  it('asks the server for the 202 path with Prefer: respond-async', async () => {
    mockFetch.mockResolvedValueOnce(json({ data: { items: [book('a')], count: 1 } }));
    await getBooks(10, 0, { search: 'x' });
    const init = mockFetch.mock.calls[0][1] as RequestInit;
    expect(new Headers(init.headers).get('Prefer')).toBe('respond-async');
  });
});
