// file: web/src/hooks/useLibraryScrollKeeper.ts
// version: 1.0.0
// guid: 4b8e2f61-9c37-4d05-a1e8-6f3c0b7d2a94
// last-edited: 2026-09-27

import { useEffect, useRef } from 'react';

/**
 * Keeps the Library's place across a browser reload and a round trip to a
 * book page (owner request 2026-09-27: "when a page reloads in the library
 * return me to where I was on the page").
 *
 * The query, filters, sort, page and page size already live in the URL
 * (Library.tsx writes them), so reload/back restore WHICH rows are shown.
 * This hook restores WHERE in them the user was: it saves the scroll offset
 * and the id of the top-visible book in sessionStorage, keyed by the URL's
 * query string, and after the rows render it scrolls that book back to the
 * top (or, if the book is no longer on the page, to the saved offset).
 *
 * The page scrolls inside MainLayout's <main> (overflow: auto), not the
 * window, which is why the browser's own scroll restoration never worked here.
 *
 * In-page refreshes do not need this: useLibraryQuery keeps the rows mounted
 * during a same-query refetch, so the scroll position is simply never lost.
 */
export const LIBRARY_SCROLL_STORAGE_PREFIX = 'library-scroll-v1:';

interface SavedPlace {
  top: number;
  bookId?: string;
}

function storageKey(viewKey: string) {
  return LIBRARY_SCROLL_STORAGE_PREFIX + viewKey;
}

function readPlace(viewKey: string): SavedPlace | null {
  try {
    const raw = sessionStorage.getItem(storageKey(viewKey));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as SavedPlace;
    return typeof parsed?.top === 'number' ? parsed : null;
  } catch {
    return null;
  }
}

function writePlace(viewKey: string, place: SavedPlace) {
  try {
    sessionStorage.setItem(storageKey(viewKey), JSON.stringify(place));
  } catch {
    // Blocked or full storage: nothing to restore next time, nothing breaks.
  }
}

/** The element that actually scrolls the Library. */
export function findScrollContainer(): HTMLElement | null {
  return (
    (document.querySelector('main') as HTMLElement | null) ??
    (document.scrollingElement as HTMLElement | null)
  );
}

/** Id of the first book row whose bottom edge is below the container's top. */
function topVisibleBookId(container: HTMLElement): string | undefined {
  const top = container.getBoundingClientRect().top;
  const rows = container.querySelectorAll<HTMLElement>('[data-book-id]');
  for (const row of rows) {
    if (row.getBoundingClientRect().bottom > top + 1) return row.dataset.bookId;
  }
  return undefined;
}

export function useLibraryScrollKeeper({
  viewKey,
  ready,
}: {
  /** Identifies the view — the Library URL's query string. */
  viewKey: string;
  /** True once the rows for viewKey are rendered. */
  ready: boolean;
}) {
  const restoredKeyRef = useRef<string | null>(null);

  // The browser restores window scroll, which is not what scrolls here, and
  // can fight the restore below; take it over while the Library is mounted.
  useEffect(() => {
    const h = window.history;
    const prev = h.scrollRestoration;
    try {
      h.scrollRestoration = 'manual';
    } catch {
      // not supported
    }
    return () => {
      try {
        h.scrollRestoration = prev;
      } catch {
        // not supported
      }
    };
  }, []);

  // Restore once per view, after its rows render.
  useEffect(() => {
    if (!ready || restoredKeyRef.current === viewKey) return;
    restoredKeyRef.current = viewKey;
    const container = findScrollContainer();
    const place = readPlace(viewKey);
    if (!container || !place) return;
    const row = place.bookId
      ? container.querySelector<HTMLElement>(`[data-book-id="${CSS.escape(place.bookId)}"]`)
      : null;
    if (row && typeof row.scrollIntoView === 'function') {
      row.scrollIntoView({ block: 'start' });
    } else {
      container.scrollTop = place.top;
    }
  }, [ready, viewKey]);

  // Save while the user scrolls — only once this view has been restored, so
  // the zero offset of a still-loading page never overwrites a real place.
  useEffect(() => {
    const container = findScrollContainer();
    if (!container) return;
    let frame = 0;
    const save = () => {
      frame = 0;
      if (restoredKeyRef.current !== viewKey) return;
      writePlace(viewKey, { top: container.scrollTop, bookId: topVisibleBookId(container) });
    };
    const onScroll = () => {
      if (frame) return;
      frame = window.requestAnimationFrame(save);
    };
    container.addEventListener('scroll', onScroll, { passive: true });
    return () => {
      container.removeEventListener('scroll', onScroll);
      // No final write here: on unmount React has already removed the rows
      // and the container may be scrolled by the next page, so a write now
      // would record the wrong place. The rAF-throttled saves above are
      // at most one frame behind the last scroll.
      if (frame) window.cancelAnimationFrame(frame);
    };
  }, [viewKey]);
}
