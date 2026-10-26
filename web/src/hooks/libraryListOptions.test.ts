// file: web/src/hooks/libraryListOptions.test.ts
// version: 1.0.1
// guid: 8b3e6d12-5f47-4a90-9c1d-2e7a0b4f6c35
// last-edited: 2026-10-26

import { describe, it, expect } from 'vitest';
import { buildLibraryListOptions, libraryTagsParam, narrowingTags } from './useLibraryQuery';
import { listFieldFilters, parseSearch } from '../utils/searchParser';
import { appendTagTerm, removeTagTerm } from '../utils/tagQuery';
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

// N-B1: a query sent whole carries its typed tag: terms in search=, so tags[] does
// not (libraryTagsParam), but the request IS narrowed by them. The tag chips and
// Cancel must read them the way the request does, or the chip shows off for a tag
// the list is narrowed by, a click appends a second term, and Cancel leaves it in
// the box.
describe('the tags a whole-sent query narrows by', () => {
  const typed = 'read_status:finished AND tag:"noir"';

  it('sends no tags[] for a typed tag term, but narrowingTags names it', () => {
    const parsed = parseSearch(typed);
    expect(parsed.serverQuery).toBe(typed);
    expect(libraryTagsParam([], parsed)).toBeUndefined();
    expect(narrowingTags([], parsed)).toEqual(['noir']);
  });

  it('is the union of the sidebar selection and the typed terms, once each', () => {
    const parsed = parseSearch('(read_status:finished tag:noir tag:gotham)');
    expect(narrowingTags(['noir', 'camelot'], parsed)).toEqual(['noir', 'camelot', 'gotham']);
    expect(libraryTagsParam(['noir', 'camelot'], parsed)).toEqual(['noir', 'camelot']);
  });

  it('leaves a negated tag term out, as the request does', () => {
    expect(narrowingTags([], parseSearch('read_status:finished AND -tag:noir'))).toEqual([]);
  });

  it('is libraryTagsParam exactly when the query is not sent whole', () => {
    for (const q of ['tag:noir Hush', 'read_status:finished tag:noir', 'tag:noir tag:gotham']) {
      expect(narrowingTags(['camelot'], parseSearch(q))).toEqual(
        libraryTagsParam(['camelot'], parseSearch(q))
      );
    }
  });

  it('does not offer a second term for a tag already in the group', () => {
    for (const q of [
      typed,
      '(read_status:finished tag:"noir")',
      '(read_status:finished tag:noir)',
    ]) {
      expect(narrowingTags([], parseSearch(q))).toEqual(['noir']);
      expect(appendTagTerm(q, 'noir')).toBe(q);
    }
  });

  it('takes the term out of a group, leaving a query that still reads', () => {
    const out = removeTagTerm('(read_status:finished tag:"noir")', 'noir');
    expect(out).toBe('(read_status:finished)');
    expect(narrowingTags([], parseSearch(out))).toEqual([]);
    expect(removeTagTerm('(tag:"noir" read_status:finished)', 'noir')).toBe(
      '(read_status:finished)'
    );
    expect(removeTagTerm('read_status:finished AND (tag:noir)', 'noir')).toBe(
      'read_status:finished AND'
    );
  });

  it('Cancel clears every typed tag term, however grouped', () => {
    const q = '(read_status:finished tag:noir) AND tag:"gotham"';
    const cleared = narrowingTags([], parseSearch(q)).reduce((acc, t) => removeTagTerm(acc, t), q);
    expect(narrowingTags([], parseSearch(cleared))).toEqual([]);
    expect(cleared).not.toContain('tag:');
  });
});
