// file: web/src/hooks/useLibraryQuery.test.ts
// version: 1.5.0
// guid: 7c8d9e0f-1a2b-4c5d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-10-06

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

describe('useLibraryQuery 400 handling (G118)', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    useLibraryCache.getState().clear();
    vi.mocked(api.getImportPaths).mockResolvedValue([]);
  });

  test('a 400 from the filter guards sets loadError (shown under the search bar), without a toast', async () => {
    const serverMessage =
      'filter on "title" has an empty value; an empty value matches every book rather than narrowing the results.';
    // vi.mock('../services/api') automocks ApiError too: its instances pass
    // the hook's `instanceof api.ApiError` check (same mocked class) but the
    // mock constructor drops message/status — restore them explicitly.
    const err = new api.ApiError(serverMessage, 400);
    Object.assign(err, { message: serverMessage, status: 400 });
    vi.mocked(api.getBooks).mockRejectedValue(err);

    const toast = vi.fn();
    const { result } = renderHook(() => useLibraryQuery(makeBaseProps({ toast })));

    await act(async () => {
      await result.current.loadAudiobooks();
    });

    // The guard's message names the field and the fix — it must reach the
    // user, not just the console (before this, a 400 rendered as a silent
    // non-retrying dead page).
    // And a 4xx is not transient: no retry may be pending.
    expect(result.current.isRetrying).toBe(false);
    // loadError stays set for the search bar's helper text and the error
    // panel (it must outlive the toast until the query is fixed). The
    // automocked ApiError is not an Error subclass, so its identity is not
    // asserted here; queryErrorMessage's own tests cover status 400.
    await waitFor(() => expect(result.current.loadError).not.toBeNull());
    // Not toasted: typing a regex produces a 400 at every debounced pause.
    expect(toast).not.toHaveBeenCalled();
  });
});

// TASK-167: the Series link's `series_id` must reach the request AND the cache
// key. The key half matters on its own: this file's hook once omitted a
// filter from the key and served the unfiltered library from a warm cache
// while the filter chip still showed it as applied.
describe('useLibraryQuery series_id (TASK-167)', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    useLibraryCache.getState().clear();
    vi.mocked(api.getImportPaths).mockResolvedValue([]);
    vi.mocked(api.getBooks).mockResolvedValue({ items: [makeBook('b1', 'One')], count: 1 });
  });

  test('passes filters.seriesId through to getBooks', async () => {
    const { result } = renderHook(() =>
      useLibraryQuery(makeBaseProps({ filters: { seriesId: 7 } }))
    );
    await act(async () => {
      await result.current.loadAudiobooks();
    });
    expect(vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.seriesId).toBe(7);
  });

  test('dropping seriesId refetches instead of answering from the series-filtered cache entry', async () => {
    const { result, rerender } = renderHook((props) => useLibraryQuery(props), {
      initialProps: makeBaseProps({ filters: { seriesId: 7 } }),
    });
    await act(async () => {
      await result.current.loadAudiobooks();
    });
    const callsWithSeries = vi.mocked(api.getBooks).mock.calls.length;
    expect(callsWithSeries).toBeGreaterThan(0);

    rerender(makeBaseProps({ filters: {} }));
    await act(async () => {
      await result.current.loadAudiobooks();
    });
    expect(vi.mocked(api.getBooks).mock.calls.length).toBeGreaterThan(callsWithSeries);
    expect(vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.seriesId).toBeUndefined();
  });
});

// Owner 2026-09-27: "why do we have to even refresh". Live books.changed events
// patch the rows on screen; nothing reloads the list or shows the spinner.
describe('useLibraryQuery live updates', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    useLibraryCache.getState().clear();
    vi.mocked(api.getImportPaths).mockResolvedValue([]);
    vi.mocked(api.getBooks).mockResolvedValue({
      items: [makeBook('a', 'A'), makeBook('b', 'B'), makeBook('c', 'C')],
      count: 3,
    });
  });

  async function loaded() {
    const hook = renderHook(() => useLibraryQuery(makeBaseProps()));
    await act(async () => {
      await hook.result.current.loadAudiobooks();
    });
    expect(hook.result.current.audiobooks.map((b) => b.id)).toEqual(['a', 'b', 'c']);
    return hook;
  }

  test('an updated event patches that row in place without refetching the list', async () => {
    const { result } = await loaded();
    const listCalls = vi.mocked(api.getBooks).mock.calls.length;
    vi.mocked(api.getBooksByIds).mockResolvedValue([makeBook('b', 'B renamed')]);
    await act(async () => {
      await result.current.applyBooksChanged({ kind: 'updated', ids: ['b', 'not-on-screen'] });
    });
    expect(vi.mocked(api.getBooksByIds)).toHaveBeenCalledWith(['b']);
    expect(result.current.audiobooks.map((b) => b.title)).toEqual(['A', 'B renamed', 'C']);
    expect(vi.mocked(api.getBooks).mock.calls.length).toBe(listCalls);
    expect(result.current.loading).toBe(false);
  });

  test('a deleted event removes the row and decrements the count', async () => {
    const { result } = await loaded();
    await act(async () => {
      await result.current.applyBooksChanged({ kind: 'deleted', ids: ['a'] });
    });
    expect(result.current.audiobooks.map((b) => b.id)).toEqual(['b', 'c']);
    expect(result.current.totalCount).toBe(2);
    expect(vi.mocked(api.getBooksByIds)).not.toHaveBeenCalled();
  });

  test('a created event only counts new books for the chip; rows do not move', async () => {
    const { result } = await loaded();
    await act(async () => {
      await result.current.applyBooksChanged({ kind: 'created', ids: ['n1', 'n2'] });
    });
    expect(result.current.newBooksCount).toBe(2);
    expect(result.current.audiobooks.map((b) => b.id)).toEqual(['a', 'b', 'c']);
    await act(async () => {
      result.current.showNewBooks();
    });
    await waitFor(() => expect(result.current.newBooksCount).toBe(0));
  });

  test('a same-query refresh keeps the rows mounted (no spinner, no scroll loss)', async () => {
    const { result } = await loaded();
    let resolve!: (v: api.BooksPage) => void;
    vi.mocked(api.getBooks).mockImplementation(
      () =>
        new Promise((r) => {
          resolve = r;
        })
    );
    act(() => {
      result.current.clearLibraryCache();
      void result.current.loadAudiobooks();
    });
    expect(result.current.loading).toBe(false);
    expect(result.current.audiobooks).toHaveLength(3);
    await act(async () => {
      resolve({ items: [makeBook('a', 'A2'), makeBook('b', 'B'), makeBook('c', 'C')], count: 3 });
    });
    await waitFor(() => expect(result.current.audiobooks[0].title).toBe('A2'));
  });

  test('a finished scan no longer reloads the list', async () => {
    const { result, rerender } = renderHook(
      (props: { scan: api.Operation | null }) =>
        useLibraryQuery(makeBaseProps({ activeScanOp: props.scan })),
      { initialProps: { scan: { id: 'op', status: 'running' } as api.Operation } }
    );
    await act(async () => {
      await result.current.loadAudiobooks();
    });
    const calls = vi.mocked(api.getBooks).mock.calls.length;
    rerender({ scan: { id: 'op', status: 'completed' } as api.Operation });
    await new Promise((r) => setTimeout(r, 0));
    expect(vi.mocked(api.getBooks).mock.calls.length).toBe(calls);
  });
});
