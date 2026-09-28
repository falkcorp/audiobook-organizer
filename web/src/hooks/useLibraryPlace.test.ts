// file: web/src/hooks/useLibraryPlace.test.ts
// version: 1.0.0
// guid: 6d2a8f47-1b39-4e05-9c7e-3a5f0b8d1c26
// last-edited: 2026-09-27

import { renderHook, act } from '@testing-library/react';
import { vi, describe, test, expect, beforeEach, afterEach } from 'vitest';
import {
  useLibrarySelection,
  LIBRARY_SELECTION_STORAGE_KEY,
} from './useLibrarySelection';
import {
  useLibraryScrollKeeper,
  LIBRARY_SCROLL_STORAGE_PREFIX,
} from './useLibraryScrollKeeper';
import type { Audiobook } from '../types';

vi.mock('../services/api');

const book = (id: string) => ({ id, title: id }) as unknown as Audiobook;
const page = [book('a'), book('b'), book('c')];

function selectionProps(audiobooks: Audiobook[] = page) {
  return {
    audiobooks,
    totalCount: 100,
    debouncedSearch: '',
    parsedSearch: null,
    filters: {},
    selectedTags: [] as string[],
    buildFieldFilters: () => [],
  };
}

describe('Library selection survives leaving and coming back', () => {
  beforeEach(() => sessionStorage.clear());

  test('select, navigate to a book (unmount), come back: still selected', () => {
    const first = renderHook(() => useLibrarySelection(selectionProps()));
    act(() => first.result.current.handleToggleSelect(page[0]));
    act(() => first.result.current.handleToggleSelect(page[2]));
    first.unmount(); // clicked into a book

    const back = renderHook(() => useLibrarySelection(selectionProps()));
    expect([...back.result.current.selectedIds].sort()).toEqual(['a', 'c']);
    expect(back.result.current.effectiveSelectedCount).toBe(2);
  });

  test('a reload (fresh hook, same tab storage) keeps it; a different page keeps it by id', () => {
    const first = renderHook(() => useLibrarySelection(selectionProps()));
    act(() => first.result.current.handleToggleSelect(page[1]));
    first.unmount();
    const otherPage = renderHook(() => useLibrarySelection(selectionProps([book('x'), book('y')])));
    expect([...otherPage.result.current.selectedIds]).toEqual(['b']);
    expect(otherPage.result.current.hasSelection).toBe(true);
  });

  test('clear empties it and the storage; deleted books drop out', () => {
    const h = renderHook(() => useLibrarySelection(selectionProps()));
    act(() => h.result.current.handleToggleSelect(page[0]));
    act(() => h.result.current.handleToggleSelect(page[1]));
    act(() => h.result.current.dropSelectedIds(['a']));
    expect([...h.result.current.selectedIds]).toEqual(['b']);
    act(() => h.result.current.handleClearSelection());
    expect(h.result.current.hasSelection).toBe(false);
    expect(sessionStorage.getItem(LIBRARY_SELECTION_STORAGE_KEY)).toBeNull();
  });

  test('unreadable storage starts empty instead of throwing', () => {
    sessionStorage.setItem(LIBRARY_SELECTION_STORAGE_KEY, '{not json');
    const h = renderHook(() => useLibrarySelection(selectionProps()));
    expect(h.result.current.hasSelection).toBe(false);
  });
});

describe('Library scroll position survives reload and back navigation', () => {
  let main: HTMLElement;
  beforeEach(() => {
    sessionStorage.clear();
    main = document.createElement('main');
    for (const id of ['a', 'b', 'c']) {
      const row = document.createElement('div');
      row.dataset.bookId = id;
      main.appendChild(row);
    }
    document.body.appendChild(main);
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
      cb(0);
      return 1;
    });
  });
  afterEach(() => {
    main.remove();
    vi.restoreAllMocks();
  });

  test('restores the saved top book after the rows render, not before', () => {
    sessionStorage.setItem(
      LIBRARY_SCROLL_STORAGE_PREFIX + 'page=3',
      JSON.stringify({ top: 900, bookId: 'b' })
    );
    const scrolled = vi.fn();
    (main.querySelector('[data-book-id="b"]') as HTMLElement).scrollIntoView = scrolled;
    const { rerender } = renderHook((p: { ready: boolean }) =>
      useLibraryScrollKeeper({ viewKey: 'page=3', ready: p.ready }), { initialProps: { ready: false } });
    expect(scrolled).not.toHaveBeenCalled();
    rerender({ ready: true });
    expect(scrolled).toHaveBeenCalledWith({ block: 'start' });
  });

  test('falls back to the saved offset when that book is no longer on the page', () => {
    sessionStorage.setItem(
      LIBRARY_SCROLL_STORAGE_PREFIX + 'page=3',
      JSON.stringify({ top: 640, bookId: 'gone' })
    );
    renderHook(() => useLibraryScrollKeeper({ viewKey: 'page=3', ready: true }));
    expect(main.scrollTop).toBe(640);
  });

  test('saves the place while scrolling, and a refetch (ready flicker) does not jump', () => {
    const h = renderHook((p: { ready: boolean }) =>
      useLibraryScrollKeeper({ viewKey: 'q=dune', ready: p.ready }), { initialProps: { ready: true } });
    main.scrollTop = 300;
    act(() => {
      main.dispatchEvent(new Event('scroll'));
    });
    const saved = JSON.parse(sessionStorage.getItem(LIBRARY_SCROLL_STORAGE_PREFIX + 'q=dune') ?? '{}');
    expect(saved.top).toBe(300);
    // An in-page refetch: the view key is unchanged, so no restore runs again.
    main.scrollTop = 310;
    h.rerender({ ready: false });
    h.rerender({ ready: true });
    expect(main.scrollTop).toBe(310);
  });

  test('takes over history scroll restoration while mounted', () => {
    window.history.scrollRestoration = 'auto';
    const h = renderHook(() => useLibraryScrollKeeper({ viewKey: '', ready: false }));
    expect(window.history.scrollRestoration).toBe('manual');
    h.unmount();
    expect(window.history.scrollRestoration).toBe('auto');
  });
});
