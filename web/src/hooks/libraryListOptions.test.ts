// file: web/src/hooks/libraryListOptions.test.ts
// version: 1.0.6
// guid: 8b3e6d12-5f47-4a90-9c1d-2e7a0b4f6c35
// last-edited: 2026-10-26

import { describe, it, expect } from 'vitest';
import { buildLibraryListOptions, libraryTagsParam } from './useLibraryQuery';
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

// A query sent whole carries its typed tag: terms in search=, where the server applies
// them as the exact tag filter, and the ones that narrow the whole query are also sent
// as tags[] (libraryTagsParam), so a chip stays exact however the rest of the box is
// written. The tag chips and Cancel read the same list, so a chip shows on for a tag
// the list is narrowed by, a click does not append a second term, and Cancel takes
// the term out of the box and leaves text the server can read.
describe('the tags a whole-sent query narrows by', () => {
  const typed = '(read_status:finished tag:"noir")';

  it('sends a typed tag term as tags[] (the exact match) and keeps it in search=', () => {
    const parsed = parseSearch(typed);
    expect(parsed.serverQuery).toBe(typed);
    expect(libraryTagsParam([], parsed)).toEqual(['noir']);
    const opts = buildLibraryListOptions({
      ...base,
      debouncedSearch: typed,
      parsedSearch: parsed,
      fieldFilters: listFieldFilters(parsed),
    });
    expect(opts.search).toBe(typed);
    expect(opts.tags).toEqual(['noir']);
  });

  it('is the union of the sidebar selection and the typed terms, once each', () => {
    const parsed = parseSearch('(read_status:finished tag:noir tag:gotham)');
    expect(libraryTagsParam(['noir', 'camelot'], parsed)).toEqual(['noir', 'camelot', 'gotham']);
  });

  it('folds a typed tag the way the server compares tags', () => {
    expect(libraryTagsParam([], parseSearch('read_status:finished tag:Noir'))).toEqual(['noir']);
  });

  it('sends no tag for a term that does not narrow the whole query', () => {
    for (const q of [
      'read_status:finished AND -tag:noir',
      'read_status:finished -(tag:noir)',
      'read_status:finished NOT (tag:noir author:kyle)',
      'read_status:finished (tag:noir OR author:kyle)',
      'read_status:finished AND tag:(noir|gotham)',
    ]) {
      expect(libraryTagsParam([], parseSearch(q)), q).toBeUndefined();
    }
  });

  it('does not offer a second term for a tag already in the group', () => {
    for (const q of [
      typed,
      '(read_status:finished tag:"noir")',
      '(read_status:finished tag:noir)',
    ]) {
      expect(libraryTagsParam([], parseSearch(q))).toEqual(['noir']);
      expect(appendTagTerm(q, 'noir')).toBe(q);
    }
  });

  it('takes the term out of a group, leaving a query that still reads', () => {
    const out = removeTagTerm('(read_status:finished tag:"noir")', 'noir');
    expect(out).toBe('(read_status:finished)');
    expect(libraryTagsParam([], parseSearch(out))).toBeUndefined();
    expect(removeTagTerm('(tag:"noir" read_status:finished)', 'noir')).toBe(
      '(read_status:finished)'
    );
    expect(removeTagTerm('read_status:finished AND (tag:noir)', 'noir')).toBe(
      'read_status:finished'
    );
  });

  it('Cancel clears every typed tag term, however grouped, and leaves a readable query', () => {
    const q = '(read_status:finished tag:noir) AND tag:"gotham"';
    const cleared = (libraryTagsParam([], parseSearch(q)) ?? []).reduce(
      (acc, t) => removeTagTerm(acc, t),
      q
    );
    expect(cleared).toBe('(read_status:finished)');
    expect(libraryTagsParam([], parseSearch(cleared))).toBeUndefined();
  });

  it('Cancel leaves an excluded tag in the box (it narrowed nothing, so it is not cleared)', () => {
    const q = 'read_status:finished -(tag:"noir") tag:"gotham"';
    const cleared = (libraryTagsParam([], parseSearch(q)) ?? []).reduce(
      (acc, t) => removeTagTerm(acc, t),
      q
    );
    expect(cleared).toBe('read_status:finished -(tag:"noir")');
  });
});

// O1: until 2026-10-26 the flat field/value split answered these with a list that
// looked narrowed and was not: `Batman -tag:"to-read"` kept the to-read books,
// `-tag:noir` returned the whole library, `tag:noir OR tag:gotham` searched for the
// word "OR" among the books with BOTH tags, and `tag:noir*` asked for a tag named
// "noir*" and showed nothing. Each is boolean structure, so each goes to the server
// whole, with nothing in filters= or tags=, and the server answers it or refuses it.
describe('a query with boolean structure reaches the server whole', () => {
  it.each(['Batman -tag:"to-read"', '-tag:noir', 'tag:noir OR tag:gotham', 'tag:noir*'])(
    '%s is search= as typed, with no filters= and no tags=',
    (typed) => {
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
    }
  );

  it('keeps a plain tag term on the flat path, as tags[] with the rest as search=', () => {
    const typed = 'Batman tag:"to-read"';
    const parsedSearch = parseSearch(typed);
    const opts = buildLibraryListOptions({
      ...base,
      debouncedSearch: typed,
      parsedSearch,
      fieldFilters: listFieldFilters(parsedSearch),
    });
    expect(opts.search).toBe('Batman');
    expect(opts.tags).toEqual(['to-read']);
  });

  it('keeps a negated field other than tag on the flat path as a negated filter', () => {
    const typed = 'Batman -title:Hush';
    const parsedSearch = parseSearch(typed);
    const opts = buildLibraryListOptions({
      ...base,
      debouncedSearch: typed,
      parsedSearch,
      fieldFilters: listFieldFilters(parsedSearch),
    });
    expect(opts.search).toBe('Batman');
    expect(JSON.parse(opts.filters ?? '[]')).toEqual([
      { field: 'title', value: 'Hush', negated: true },
    ]);
  });

  // B1: a regex or a value holding ( ) or | is a filter. Sent whole it reached the search
  // index, which has no regex, and answered 0 rows or the wrong rows with no message.
  it.each([
    [
      'title:/^(the|a) /',
      [{ field: 'title', value: '/^(the|a) /', negated: false, quoted: false }],
    ],
    ['-title:/^(?:\\d)/', [{ field: 'title', value: '/^(?:\\d)/', negated: true, quoted: false }]],
    [
      'description:/(?i)gotham/',
      [{ field: 'description', value: '/(?i)gotham/', negated: false, quoted: false }],
    ],
    [
      'metadata:/(audible|google)/',
      [{ field: 'metadata', value: '/(audible|google)/', negated: false, quoted: false }],
    ],
    [
      'title:/hush|knight/',
      [{ field: 'title', value: '/hush|knight/', negated: false, quoted: false }],
    ],
    [
      'title:Batman(1989)',
      [{ field: 'title', value: 'Batman(1989)', negated: false, quoted: false }],
    ],
  ])('%s travels as a filter, with no search= text and no whole send', (typed, expected) => {
    const parsedSearch = parseSearch(typed);
    const opts = buildLibraryListOptions({
      ...base,
      debouncedSearch: typed,
      parsedSearch,
      fieldFilters: listFieldFilters(parsedSearch),
    });
    expect(opts.search).toBeUndefined();
    expect(
      (JSON.parse(opts.filters ?? '[]') as Array<Record<string, unknown>>).map((f) => ({
        quoted: false,
        ...f,
      }))
    ).toEqual(expected);
  });

  // B1': an operator among free words alone is the server's to read from search=; the
  // field term beside it stays a filter, as it was on main
  it.each([
    ['title:bat NOT robin', 'title', 'bat', 'NOT robin'],
    ['title:bat Batman (1987)', 'title', 'bat', 'Batman (1987)'],
    ['title:/^the/ NOT robin', 'title', '/^the/', 'NOT robin'],
    ['codec:aac NOT dramatized', 'codec', 'aac', 'NOT dramatized'],
    ['ident:missing NOT batman', 'ident', 'missing', 'NOT batman'],
    // B2: AND and && are the split's own meaning
    ['title:bat AND year', 'title', 'bat', 'AND year'],
    ['title:/^bat/ AND year', 'title', '/^bat/', 'AND year'],
    ['codec:aac AND year', 'codec', 'aac', 'AND year'],
  ])(
    '%s splits: the %s filter stays a filter and the rest is search=',
    (typed, field, value, rest) => {
      const parsedSearch = parseSearch(typed);
      const opts = buildLibraryListOptions({
        ...base,
        debouncedSearch: typed,
        parsedSearch,
        fieldFilters: listFieldFilters(parsedSearch),
      });
      expect(opts.search).toBe(rest);
      expect(JSON.parse(opts.filters ?? '[]')).toEqual([{ field, value, negated: false }]);
    }
  );

  it('carries a regex beside a per-user term as filters too', () => {
    const typed = 'read_status:finished title:/^(the|a) /';
    const parsedSearch = parseSearch(typed);
    const opts = buildLibraryListOptions({
      ...base,
      debouncedSearch: typed,
      parsedSearch,
      fieldFilters: listFieldFilters(parsedSearch),
    });
    expect(opts.search).toBeUndefined();
    expect(JSON.parse(opts.filters ?? '[]')).toEqual([
      { field: 'read_status', value: 'finished', negated: false },
      { field: 'title', value: '/^(the|a) /', negated: false },
    ]);
  });

  // N-N5: the tag chip reads `tag:noir ^2` as off, so the list must not be filtered by
  // the tag while it says so: the whole text goes to the server, which refuses it.
  it.each(['tag:noir ^2', 'tag:"noir" ^2', 'tag:noir ~'])(
    '%s is search= as typed, with no tags=',
    (typed) => {
      const parsedSearch = parseSearch(typed);
      const opts = buildLibraryListOptions({
        ...base,
        debouncedSearch: typed,
        parsedSearch,
        fieldFilters: listFieldFilters(parsedSearch),
      });
      expect(opts.search).toBe(typed);
      expect(opts.tags).toBeUndefined();
      expect(opts.filters).toBeUndefined();
    }
  );
});
