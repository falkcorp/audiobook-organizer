// file: web/src/hooks/useLibraryQuery.test.ts
// version: 1.1.0
// guid: 7c8d9e0f-1a2b-4c5d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-07-11

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

// baseProps mirrors the fixture in the describe block above — duplicated
// rather than shared across describe blocks so each suite's mock wiring
// stays self-contained and easy to read in isolation.
function makeBaseProps(overrides: Partial<Parameters<typeof useLibraryQuery>[0]> = {}) {
  return {
    page: 1,
    itemsPerPage: 20,
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
    ...overrides,
  };
}

// abortableGetBooks returns a getBooks mock whose promise only settles when
// the AbortSignal passed by loadAudiobooks() fires — modeling a real fetch()
// cancellation instead of a fake timer or a manually-resolved promise.
function abortableGetBooks() {
  let capturedSignal: AbortSignal | undefined;
  const impl: typeof api.getBooks = (_limit, _offset, options) => {
    capturedSignal = options?.signal;
    return new Promise((_resolve, reject) => {
      options?.signal?.addEventListener('abort', () => {
        const err = new Error('The operation was aborted.');
        err.name = 'AbortError';
        reject(err);
      });
    });
  };
  return { impl, getSignal: () => capturedSignal };
}

describe('useLibraryQuery cancelLoad', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    useLibraryCache.getState().clear();
    vi.mocked(api.getImportPaths).mockResolvedValue([]);
  });

  test('cancelLoad aborts the in-flight fetch and flips loading off immediately', async () => {
    const { impl, getSignal } = abortableGetBooks();
    vi.mocked(api.getBooks).mockImplementation(impl);

    const { result } = renderHook(() => useLibraryQuery(makeBaseProps()));

    act(() => {
      result.current.loadAudiobooks();
    });
    await waitFor(() => expect(result.current.loading).toBe(true));
    expect(getSignal()?.aborted).toBe(false);

    act(() => {
      result.current.cancelLoad();
    });

    // loading flips synchronously — cancelLoad does not wait for the
    // aborted fetch promise to reject and run through its own finally.
    expect(result.current.loading).toBe(false);
    expect(getSignal()?.aborted).toBe(true);
  });

  test('an aborted request does not surface an error toast or clear the book list', async () => {
    const { impl } = abortableGetBooks();
    vi.mocked(api.getBooks).mockImplementation(impl);
    const toast = vi.fn();

    const { result } = renderHook(() => useLibraryQuery(makeBaseProps({ toast })));

    act(() => {
      result.current.loadAudiobooks();
    });
    await waitFor(() => expect(result.current.loading).toBe(true));

    act(() => {
      result.current.cancelLoad();
    });

    // Let the now-rejected fetch promise's .catch() handler run.
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(toast).not.toHaveBeenCalled();
    // The abort branch returns before the `setAudiobooks([])` fallback that
    // a genuine failure would hit, so a cancel is not indistinguishable
    // from a server error in the UI.
    expect(result.current.audiobooks).toEqual([]);
  });

  test('loadAudiobooks aborts a still-in-flight prior call before issuing a new one', async () => {
    const { impl, getSignal: getFirstSignal } = abortableGetBooks();
    vi.mocked(api.getBooks).mockImplementationOnce(impl);
    vi.mocked(api.getBooks).mockResolvedValueOnce({
      items: [makeBook('second', 'Second Book')],
      count: 1,
    });

    const { result } = renderHook(() => useLibraryQuery(makeBaseProps()));

    act(() => {
      result.current.loadAudiobooks();
    });
    await waitFor(() => expect(getFirstSignal()).toBeDefined());
    expect(getFirstSignal()?.aborted).toBe(false);

    act(() => {
      result.current.loadAudiobooks();
    });

    await waitFor(() => expect(getFirstSignal()?.aborted).toBe(true));
    await waitFor(() => expect(result.current.audiobooks).toHaveLength(1));
    expect(result.current.audiobooks[0].id).toBe('second');
  });
});
