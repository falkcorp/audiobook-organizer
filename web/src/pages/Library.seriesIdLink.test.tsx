// file: web/src/pages/Library.seriesIdLink.test.tsx
// version: 1.0.0
// guid: 8dabbf78-942b-49cc-a8a6-224e6697a42f
// last-edited: 2026-09-12

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { Library } from './Library';
import * as api from '../services/api';
import { useLibraryCache } from '../stores/useLibraryCache';

vi.mock('../services/api', () => {
  class ApiError extends Error {
    status: number;
    data?: unknown;
    constructor(message: string, status: number, data?: unknown) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
      this.data = data;
    }
  }
  return {
    ApiError,
    getOperationLogsTail: vi.fn().mockResolvedValue([]),
    getOperationTimeline: vi.fn().mockResolvedValue([]),
    getActiveOperations: vi.fn().mockResolvedValue([]),
    getBooks: vi.fn().mockResolvedValue({
      items: [
        {
          id: 'id-1',
          title: 'Test Book',
          file_path: '/tmp/book.m4b',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          author_name: 'Author',
        },
      ],
      count: 1,
    }),
    searchBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    searchBooksPage: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getImportPaths: vi.fn().mockResolvedValue([]),
    countBooks: vi.fn().mockResolvedValue(1),
    getBookFacets: vi.fn().mockResolvedValue({ genres: [], languages: [] }),
    getAuthors: vi.fn().mockResolvedValue([]),
    getSeries: vi.fn().mockResolvedValue([]),
    getSystemStatus: vi.fn().mockResolvedValue({
      status: 'ok',
      library: { path: '/tmp', book_count: 1, total_size: 0 },
      import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
      memory: {},
      runtime: {},
      operations: { recent: [] },
    }),
    getHomeDirectory: vi.fn().mockResolvedValue('/tmp'),
    getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getUserColumnConfig: vi.fn().mockResolvedValue(null),
    saveUserColumnConfig: vi.fn().mockResolvedValue(undefined),
    listAllUserTags: vi.fn().mockResolvedValue([]),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

// Reports the query string the page itself last wrote. Library's URL-write
// effect rebuilds the whole query string from state (it always adds page=N),
// so a filter param that effect does not write is stripped here even though
// the books query was narrowed on the first load.
function LocationProbe() {
  const { search } = useLocation();
  return <div data-testid="location-search">{search}</div>;
}

describe('Library series_id URL round-trip (TASK-167)', () => {
  // The library cache is a module-level store shared by every test in this
  // file. Without clearing it, a test that ends up caching the no-series page
  // makes the next test's load a cache hit that never calls getBooks.
  beforeEach(() => {
    vi.clearAllMocks();
    useLibraryCache.getState().clear();
  });

  it('narrows the books query to series_id from the URL and keeps it in the URL it writes back', async () => {
    render(
      <MemoryRouter initialEntries={['/library?series_id=7']}>
        <Library />
        <LocationProbe />
      </MemoryRouter>
    );

    // Read half: the request carries the series.
    await waitFor(() => {
      expect(vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.seriesId).toBe(7);
    });

    // Write half: page=1 proves the write effect ran; series_id must survive it.
    await waitFor(() => {
      const written = new URLSearchParams(screen.getByTestId('location-search').textContent ?? '');
      expect(written.get('page')).toBe('1');
      expect(written.get('series_id')).toBe('7');
    });
  });

  it('sends no series filter when the URL has none', async () => {
    render(
      <MemoryRouter initialEntries={['/library']}>
        <Library />
      </MemoryRouter>
    );

    await waitFor(() => expect(api.getBooks).toHaveBeenCalled());
    expect(vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.seriesId).toBeUndefined();
  });
});
