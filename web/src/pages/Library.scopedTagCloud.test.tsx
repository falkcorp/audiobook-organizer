// file: web/src/pages/Library.scopedTagCloud.test.tsx
// version: 1.0.0
// guid: 0d7b4f2a-8c3e-4a19-b6d5-2e9f1c7a3b84
// last-edited: 2026-10-06

import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
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
    getSeries: vi
      .fn()
      .mockResolvedValue([
        {
          id: 42,
          name: 'The Expanse',
          created_at: '2026-01-01T00:00:00Z',
          book_count: 9,
          file_count: 9,
        },
      ]),
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
    listAllUserTags: vi.fn().mockResolvedValue([
      { tag: 'fantasy', count: 12306 },
      { tag: 'epic', count: 4000 },
      { tag: 'romance', count: 3000 },
    ]),
    // Scoped answer: depends on the request, like the server's.
    getScopedTagFacets: vi.fn(async (opts?: { search?: string; tags?: string[] }) => {
      const tags = opts?.tags ?? [];
      if (tags.includes('fantasy')) {
        return {
          tags: [
            { tag: 'fantasy', count: 9 },
            { tag: 'epic', count: 3 },
          ],
          total: 9,
        };
      }
      return {
        tags: [
          { tag: 'fantasy', count: 20 },
          { tag: 'epic', count: 5 },
        ],
        total: 25,
      };
    }),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

const lastBooksCall = () => vi.mocked(api.getBooks).mock.calls.at(-1)?.[2];
const lastFacetsCall = () => vi.mocked(api.getScopedTagFacets).mock.calls.at(-1)?.[0];

describe('Library Browse-by-Tag chips are scoped to the current results', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useLibraryCache.getState().clear();
    try {
      localStorage.clear();
    } catch {
      // ignore
    }
  });

  it('shows counts over the current results, not the library-wide totals', async () => {
    render(
      <MemoryRouter initialEntries={['/library']}>
        <Library />
      </MemoryRouter>
    );
    expect(await screen.findByText('fantasy (20)', {}, { timeout: 3000 })).toBeInTheDocument();
    expect(screen.queryByText('fantasy (12306)')).toBeNull();
    // A tag absent from the results is not offered.
    expect(screen.queryByText(/^romance/)).toBeNull();
  });

  it('refetches the facets with the query the list runs', async () => {
    render(
      <MemoryRouter initialEntries={['/library?search=dune']}>
        <Library />
      </MemoryRouter>
    );
    await waitFor(() => expect(lastFacetsCall()?.search).toBe('dune'), { timeout: 3000 });
    // Same predicate as the list request.
    await waitFor(() => expect(lastBooksCall()?.search).toBe('dune'), { timeout: 3000 });
    expect(lastFacetsCall()?.isPrimaryVersion).toBe(lastBooksCall()?.isPrimaryVersion);
    expect(lastFacetsCall()?.tags).toEqual(lastBooksCall()?.tags);
  });

  it('a chip click appends tag:"x" to the search and each further chip narrows', async () => {
    render(
      <MemoryRouter initialEntries={['/library']}>
        <Library />
      </MemoryRouter>
    );
    fireEvent.click(await screen.findByText('fantasy (20)', {}, { timeout: 3000 }));

    await waitFor(
      () => expect(screen.getByDisplayValue('tag:"fantasy"')).toBeInTheDocument(),
      { timeout: 3000 }
    );
    await waitFor(() => expect(lastBooksCall()?.tags).toEqual(['fantasy']), { timeout: 3000 });
    await waitFor(() => expect(lastFacetsCall()?.tags).toEqual(['fantasy']), { timeout: 3000 });

    // The cloud now reflects the narrowed set.
    fireEvent.click(await screen.findByText('epic (3)', {}, { timeout: 3000 }));
    await waitFor(
      () => expect(screen.getByDisplayValue('tag:"fantasy" tag:"epic"')).toBeInTheDocument(),
      { timeout: 3000 }
    );
    // Both tags reach the server: the second chip narrows instead of being dropped.
    await waitFor(() => expect(lastBooksCall()?.tags).toEqual(['fantasy', 'epic']), {
      timeout: 3000,
    });
  });
});
