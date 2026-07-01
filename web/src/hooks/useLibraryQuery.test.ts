// file: web/src/hooks/useLibraryQuery.test.ts
// version: 1.0.0
// guid: 7c8d9e0f-1a2b-4c5d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-07-01

import { renderHook, act, waitFor } from '@testing-library/react';
import { vi, describe, test, expect, beforeEach } from 'vitest';
import { useLibraryQuery } from './useLibraryQuery';
import * as api from '../services/api';
import { useLibraryCache } from '../stores/useLibraryCache';
import { SortField, SortOrder } from '../types';
import type { Audiobook } from '../types';

vi.mock('../services/api');

function makeBook(id: string, title: string): api.Book {
  return { id, title } as unknown as api.Book;
}

function convertBook(book: api.Book): Audiobook {
  return { id: book.id, title: book.title } as unknown as Audiobook;
}

describe('useLibraryQuery out-of-order response guard', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    useLibraryCache.getState().clear();
    vi.mocked(api.getImportPaths).mockResolvedValue([]);
  });

  test('a slower stale request does not overwrite a faster, newer request', async () => {
    // First (stale) call: page 2 @ itemsPerPage 20 -> resolves LAST.
    // Second (correct) call: page 1 @ itemsPerPage 500 -> resolves FIRST.
    let resolveStale!: (v: api.BooksPage) => void;
    let resolveFresh!: (v: api.BooksPage) => void;

    vi.mocked(api.getBooks).mockImplementation((_limit, offset) => {
      if (offset === 20) {
        return new Promise((resolve) => {
          resolveStale = resolve;
        });
      }
      return new Promise((resolve) => {
        resolveFresh = resolve;
      });
    });

    const baseProps = {
      debouncedSearch: '',
      parsedSearch: null,
      filters: {},
      selectedTags: [] as string[],
      sortBy: SortField.Title,
      sortOrder: SortOrder.Ascending,
      activeScanOp: null,
      activeOrganizeOp: null,
      setImportPaths: vi.fn(),
      navigate: vi.fn() as unknown as ReturnType<typeof import('react-router-dom').useNavigate>,
      toast: vi.fn(),
      buildFieldFilters: () => [],
      convertBook,
    };

    const { result, rerender } = renderHook(
      (props: { page: number; itemsPerPage: number }) =>
        useLibraryQuery({ ...baseProps, ...props }),
      { initialProps: { page: 2, itemsPerPage: 20 } }
    );

    // Kick off the stale request (offset = (2-1)*20 = 20).
    act(() => {
      result.current.loadAudiobooks();
    });

    // Switch to the corrected page/size and kick off the fresh request
    // (offset = (1-1)*500 = 0) before the stale one resolves.
    rerender({ page: 1, itemsPerPage: 500 });
    act(() => {
      result.current.loadAudiobooks();
    });

    // Fresh response lands first...
    resolveFresh({ items: [makeBook('!fresh', '!Fresh Book')], count: 1 });
    await waitFor(() => expect(result.current.audiobooks).toHaveLength(1));
    expect(result.current.audiobooks[0].id).toBe('!fresh');

    // ...then the stale response lands late. It must be dropped, not applied.
    act(() => {
      resolveStale({ items: [makeBook('stale', 'Stale Book')], count: 1 });
    });
    await new Promise((r) => setTimeout(r, 0));

    expect(result.current.audiobooks).toHaveLength(1);
    expect(result.current.audiobooks[0].id).toBe('!fresh');
  });
});
