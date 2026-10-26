// file: web/src/hooks/libraryListOptions.test.ts
// version: 1.0.0
// guid: 8b3e6d12-5f47-4a90-9c1d-2e7a0b4f6c35
// last-edited: 2026-10-26

import { describe, it, expect } from 'vitest';
import { buildLibraryListOptions, libraryTagsParam } from './useLibraryQuery';
import { listFieldFilters, parseSearch } from '../utils/searchParser';
import { SortField, SortOrder } from '../types';

const base = {
  filters: {},
  selectedTags: [] as string[],
  sortBy: SortField.Title,
  sortOrder: SortOrder.Ascending,
};

describe('the Library list request for a query the server must read whole', () => {
  it('carries the typed query as search= and nothing as filters= or tags=', () => {
    const typed = 'read_status:finished OR tag:noir';
    const parsedSearch = parseSearch(typed);
    const opts = buildLibraryListOptions({
      ...base,
      debouncedSearch: typed,
      parsedSearch,
      fieldFilters: listFieldFilters(parsedSearch),
    });
    expect(opts.search).toBe(typed);
    expect(opts.filters).toBeUndefined();
    expect(opts.tags).toBeUndefined();
  });

  it('splits an ordinary query into search, filters and tags as before', () => {
    const typed = 'read_status:finished tag:noir Hush';
    const parsedSearch = parseSearch(typed);
    const opts = buildLibraryListOptions({
      ...base,
      debouncedSearch: typed,
      parsedSearch,
      fieldFilters: listFieldFilters(parsedSearch),
    });
    expect(opts.search).toBe('Hush');
    expect(JSON.parse(opts.filters ?? '[]')).toEqual([
      { field: 'read_status', value: 'finished', negated: false },
    ]);
    expect(opts.tags).toEqual(['noir']);
  });

  it('still narrows by a selected tag chip when the server reads the query', () => {
    expect(libraryTagsParam(['noir'], parseSearch('(read_status:finished title:Hush)'))).toEqual([
      'noir',
    ]);
  });
});
