// file: web/src/hooks/useLibrarySelection.ts
// version: 1.0.0
// guid: e5f6a7b8-c9d0-1234-ef01-234567890104
// last-edited: 2026-06-22

import { useState, useCallback, useRef } from 'react';
import React from 'react';
import * as api from '../services/api';
import type { Audiobook } from '../types';
import type { ParsedSearch } from '../utils/searchParser';

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

export function useLibrarySelection({
  audiobooks,
  totalCount,
  debouncedSearch,
  parsedSearch,
  filters,
  selectedTags,
  buildFieldFilters,
}: UseLibrarySelectionParams) {
  const [selectedAudiobooks, setSelectedAudiobooks] = useState<Audiobook[]>([]);
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
    allOnPageSelected && crossPageFilter === null && selectedAudiobooks.length < totalCount && totalCount > audiobooks.length;

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

  const handleSelectAllItems = useCallback(() => {
    const fieldFilters = buildFieldFilters();
    const searchText = parsedSearch ? parsedSearch.freeText : debouncedSearch;
    let tagsForFilter: string[] | undefined;
    if (selectedTags && selectedTags.length > 0) {
      tagsForFilter = selectedTags;
    } else {
      const parsedTag = parsedSearch?.fieldFilters.find((f) => f.field === 'tag' && !f.negated)?.value;
      if (parsedTag) tagsForFilter = [parsedTag];
    }
    const libraryState = filters.libraryState === 'deleted' ? undefined : filters.libraryState;

    const filterSpec: api.SelectionSpec['filter'] = {};
    if (searchText) filterSpec.search = searchText;
    if (tagsForFilter && tagsForFilter.length > 0) {
      filterSpec.tags = tagsForFilter;
      // back-compat single-tag field
      filterSpec.tag = tagsForFilter[0];
    }
    if (libraryState) filterSpec.library_state = libraryState;
    if (fieldFilters.length > 0) filterSpec.field_filters = fieldFilters;

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
    handleSelectAllItems,
  };
}
