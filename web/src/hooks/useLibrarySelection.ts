// file: web/src/hooks/useLibrarySelection.ts
// version: 1.1.2
// guid: e5f6a7b8-c9d0-1234-ef01-234567890104
// last-edited: 2026-10-26

import { useState, useCallback, useRef, useEffect, useMemo } from 'react';
import React from 'react';
import * as api from '../services/api';
import type { Audiobook } from '../types';
import { listSearchText, type ParsedSearch } from '../utils/searchParser';
import { libraryTagsParam } from './useLibraryQuery';

interface UseLibrarySelectionFilters {
  author?: string;
  series?: string;
  genre?: string;
  language?: string;
  libraryState?: string;
}

interface UseLibrarySelectionParams {
  audiobooks: Audiobook[];
  totalCount: number;
  debouncedSearch: string;
  parsedSearch: ParsedSearch | null;
  filters: UseLibrarySelectionFilters;
  selectedTags: string[];
  buildFieldFilters: () => Array<{ field: string; value: string; negated: boolean }>;
}

/**
 * sessionStorage key for the Library's multi-select. Selection is by book id
 * (the rows are kept so bulk actions can read their state) and survives
 * clicking into a book and back, a browser reload, and page/filter changes
 * (owner request 2026-09-27: "hold my selections when I mistakenly click an
 * icon"). sessionStorage, not localStorage: a selection is a working state of
 * this tab, not a preference.
 */
export const LIBRARY_SELECTION_STORAGE_KEY = 'library-selection-v1';

function readStoredSelection(): Audiobook[] {
  try {
    const raw = sessionStorage.getItem(LIBRARY_SELECTION_STORAGE_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed)
      ? (parsed.filter((b) => b && typeof (b as Audiobook).id === 'string') as Audiobook[])
      : [];
  } catch {
    return [];
  }
}

function writeStoredSelection(books: Audiobook[]) {
  try {
    if (books.length === 0) sessionStorage.removeItem(LIBRARY_SELECTION_STORAGE_KEY);
    else sessionStorage.setItem(LIBRARY_SELECTION_STORAGE_KEY, JSON.stringify(books));
  } catch {
    // Blocked or full storage: the selection still works for this visit, but
    // an OLDER stored selection must not survive to be restored later.
    try {
      sessionStorage.removeItem(LIBRARY_SELECTION_STORAGE_KEY);
    } catch {
      // storage unavailable entirely
    }
  }
}

/**
 * The filter a cross-page "select all" sends: the same predicate the list on
 * screen was built from, so the selection (which feeds bulk actions) holds the
 * books the person sees. Search text and tags come from the helpers the list
 * request uses (listSearchText, libraryTagsParam), so a query the server reads
 * whole carries its tag: terms in `search=` and no tag is applied a second time.
 */
export function buildSelectAllFilter(args: {
  fieldFilters: Array<{ field: string; value: string; negated: boolean }>;
  parsedSearch: ParsedSearch | null;
  debouncedSearch: string;
  selectedTags: string[] | undefined;
  libraryState: string | undefined;
}): api.SelectionSpec['filter'] {
  const { fieldFilters, parsedSearch, debouncedSearch, selectedTags, libraryState } = args;
  const searchText = listSearchText(parsedSearch, debouncedSearch);
  const tagsForFilter = libraryTagsParam(selectedTags, parsedSearch);
  // 'deleted' is a client-side concept (marked_for_deletion flag); send no library_state to server
  const serverLibraryState = libraryState === 'deleted' ? undefined : libraryState;

  const filterSpec: api.SelectionSpec['filter'] = {};
  if (searchText) filterSpec.search = searchText;
  if (tagsForFilter && tagsForFilter.length > 0) {
    filterSpec.tags = tagsForFilter;
    // back-compat single-tag field
    filterSpec.tag = tagsForFilter[0];
  }
  if (serverLibraryState) filterSpec.library_state = serverLibraryState;
  if (fieldFilters.length > 0) filterSpec.field_filters = fieldFilters;
  return filterSpec;
}

export function useLibrarySelection({
  audiobooks,
  totalCount,
  debouncedSearch,
  parsedSearch,
  filters,
  selectedTags,
  buildFieldFilters,
}: UseLibrarySelectionParams) {
  const [storedSelection, setSelectedAudiobooks] = useState<Audiobook[]>(readStoredSelection);
  useEffect(() => {
    writeStoredSelection(storedSelection);
  }, [storedSelection]);

  // Selected rows as they are NOW: a row that is on screen reads from the
  // current page (a live update may have patched it), so bulk-action guards
  // (selectedHasDeleted, ...) never act on a stale copy.
  const selectedAudiobooks = useMemo(() => {
    if (audiobooks.length === 0) return storedSelection;
    const onScreen = new Map(audiobooks.map((b) => [b.id, b]));
    return storedSelection.map((b) => onScreen.get(b.id) ?? b);
  }, [storedSelection, audiobooks]);
  const [crossPageFilter, setCrossPageFilter] = useState<api.SelectionSpec['filter'] | null>(null);

  const lastSelectedIndexRef = useRef<number>(-1);

  const selectedIds = new Set(selectedAudiobooks.map((book) => book.id));
  const effectiveSelectedIds: string[] = selectedAudiobooks.map((b) => b.id);
  const effectiveSelectedCount = crossPageFilter !== null ? totalCount : selectedAudiobooks.length;
  const hasSelection = effectiveSelectedCount > 0;
  const allOnPageSelected =
    audiobooks.length > 0 && audiobooks.every((book) => selectedIds.has(book.id));
  const someOnPageSelected = audiobooks.some((book) => selectedIds.has(book.id));
  const selectedHasDeleted = selectedAudiobooks.some((book) => book.marked_for_deletion);
  const selectedHasActive = selectedAudiobooks.some((book) => !book.marked_for_deletion);
  const selectedHasImport = selectedAudiobooks.some((book) => book.library_state === 'imported');
  const showSelectAllBanner =
    allOnPageSelected &&
    crossPageFilter === null &&
    selectedAudiobooks.length < totalCount &&
    totalCount > audiobooks.length;

  const handleToggleSelect = (audiobook: Audiobook, event?: React.MouseEvent) => {
    // Any individual toggle exits cross-page-select-all mode.
    setCrossPageFilter(null);
    const clickedIndex = audiobooks.findIndex((b) => b.id === audiobook.id);

    // Shift-click: select range from last selected to clicked
    if (event?.shiftKey && lastSelectedIndexRef.current >= 0 && clickedIndex >= 0) {
      const start = Math.min(lastSelectedIndexRef.current, clickedIndex);
      const end = Math.max(lastSelectedIndexRef.current, clickedIndex);
      const rangeBooks = audiobooks.slice(start, end + 1);
      setSelectedAudiobooks((prev) => {
        const byId = new Map(prev.map((b) => [b.id, b]));
        for (const b of rangeBooks) {
          byId.set(b.id, b);
        }
        return Array.from(byId.values());
      });
      lastSelectedIndexRef.current = clickedIndex;
      return;
    }

    // Normal click: toggle single
    setSelectedAudiobooks((prev) => {
      if (prev.some((selected) => selected.id === audiobook.id)) {
        return prev.filter((selected) => selected.id !== audiobook.id);
      }
      return [...prev, audiobook];
    });
    lastSelectedIndexRef.current = clickedIndex;
  };

  const handleSelectAllOnPage = () => {
    setCrossPageFilter(null);
    setSelectedAudiobooks((prev) => {
      const byId = new Map(prev.map((book) => [book.id, book]));
      audiobooks.forEach((book) => {
        if (!byId.has(book.id)) {
          byId.set(book.id, book);
        }
      });
      return Array.from(byId.values());
    });
  };

  const handleToggleSelectAllOnPage = () => {
    setCrossPageFilter(null);
    if (allOnPageSelected) {
      setSelectedAudiobooks((prev) =>
        prev.filter((book) => !audiobooks.some((pageBook) => pageBook.id === book.id))
      );
      return;
    }
    handleSelectAllOnPage();
  };

  const handleClearSelection = () => {
    setCrossPageFilter(null);
    setSelectedAudiobooks([]);
  };

  /** Drop ids of books that no longer exist (a books.changed "deleted" event). */
  const dropSelectedIds = useCallback((ids: string[]) => {
    if (ids.length === 0) return;
    const gone = new Set(ids);
    setSelectedAudiobooks((prev) =>
      prev.some((b) => gone.has(b.id)) ? prev.filter((b) => !gone.has(b.id)) : prev
    );
  }, []);

  const handleSelectAllItems = useCallback(() => {
    const filterSpec = buildSelectAllFilter({
      fieldFilters: buildFieldFilters(),
      parsedSearch,
      debouncedSearch,
      selectedTags,
      libraryState: filters.libraryState,
    });

    setCrossPageFilter(filterSpec);
  }, [buildFieldFilters, debouncedSearch, filters, parsedSearch, selectedTags]);

  return {
    selectedAudiobooks,
    setSelectedAudiobooks,
    crossPageFilter,
    setCrossPageFilter,
    selectedIds,
    effectiveSelectedIds,
    effectiveSelectedCount,
    hasSelection,
    allOnPageSelected,
    someOnPageSelected,
    selectedHasDeleted,
    selectedHasActive,
    selectedHasImport,
    showSelectAllBanner,
    lastSelectedIndexRef,
    handleToggleSelect,
    handleSelectAllOnPage,
    handleToggleSelectAllOnPage,
    handleClearSelection,
    dropSelectedIds,
    handleSelectAllItems,
  };
}
