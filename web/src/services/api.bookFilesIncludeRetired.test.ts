// file: web/src/services/api.bookFilesIncludeRetired.test.ts
// version: 1.0.0
// guid: 91c4e7d2-3a58-4b06-8f1d-7e2a5c60b9d3
// last-edited: 2026-10-23

import { afterEach, describe, expect, it, vi } from 'vitest';
import { getBookFiles } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubFetch(): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn(
    async () =>
      new Response(JSON.stringify({ data: { files: [], count: 0 } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
  );
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

describe('getBookFiles includeRetired', () => {
  it('sends include_retired=true only when asked', async () => {
    const fetchMock = stubFetch();
    await getBookFiles('b1', { includeRetired: true, skipDiskCheck: true });
    const url = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost');
    expect(url.pathname).toBe('/api/v1/audiobooks/b1/files');
    expect(url.searchParams.get('include_retired')).toBe('true');
    expect(url.searchParams.get('disk_check')).toBe('false');
  });

  it('leaves the parameter off by default', async () => {
    const fetchMock = stubFetch();
    await getBookFiles('b1');
    await getBookFiles('b1', { includeRetired: false });
    for (const call of fetchMock.mock.calls) {
      expect(String(call[0])).not.toContain('include_retired');
    }
  });
});
