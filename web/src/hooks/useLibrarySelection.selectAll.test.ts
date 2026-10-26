// file: web/src/hooks/useLibrarySelection.selectAll.test.ts
// version: 1.0.0
// guid: 2d7f4a90-6c13-4e85-b0a2-9e5c1f8d3b67
// last-edited: 2026-10-26

import { describe, it, expect } from 'vitest';
import { buildSelectAllFilter } from './useLibrarySelection';
import { buildLibraryListOptions } from './useLibraryQuery';
import { listFieldFilters, parseSearch } from '../utils/searchParser';
import { SortField, SortOrder } from '../types';

function spec(typed: string, selectedTags: string[] = [], libraryState?: string) {
  const parsedSearch = parseSearch(typed);
  return buildSelectAllFilter({
    fieldFilters: listFieldFilters(parsedSearch),
    parsedSearch,
    debouncedSearch: typed,
    selectedTags,
    libraryState,
  });
}

describe('buildSelectAllFilter', () => {
  it('holds the same predicate as the list for a query the server reads whole', () => {
    const typed = 'read_status:finished OR tag:noir';
    const filter = spec(typed);
    // The server reads the whole query, tag: term included, so no tag is ANDed on
    // top of an OR the person typed.
    expect(filter).toEqual({ search: typed });
  });

  it('agrees with buildLibraryListOptions on search, tags and filters', () => {
    for (const typed of [
      'read_status:finished OR tag:noir',
      '(read_status:finished title:Hush) tag:noir',
      'read_status:finished tag:noir tag:gotham Hush',
    ]) {
      const parsedSearch = parseSearch(typed);
      const fieldFilters = listFieldFilters(parsedSearch);
      const list = buildLibraryListOptions({
        debouncedSearch: typed,
        parsedSearch,
        filters: {},
        selectedTags: [],
        sortBy: SortField.Title,
        sortOrder: SortOrder.Ascending,
        fieldFilters,
      });
      const all = spec(typed);
      expect(all?.search, typed).toBe(list.search);
      expect(all?.tags, typed).toEqual(list.tags);
      expect(all?.field_filters, typed).toEqual(fieldFilters.length > 0 ? fieldFilters : undefined);
    }
  });

  it('sends every typed tag, not only the first, and a selected chip with them', () => {
    expect(spec('tag:noir tag:gotham Hush')?.tags).toEqual(['noir', 'gotham']);
    expect(spec('tag:noir', ['gotham'])?.tags).toEqual(['gotham', 'noir']);
  });

  it('leaves the deleted pseudo-state to the client', () => {
    expect(spec('Hush', [], 'deleted')?.library_state).toBeUndefined();
    expect(spec('Hush', [], 'imported')?.library_state).toBe('imported');
  });
});
