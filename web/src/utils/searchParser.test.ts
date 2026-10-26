// file: web/src/utils/searchParser.test.ts
// version: 1.2.3
// guid: 3c7e9a15-6d2f-4b84-a0e3-9f1b5c8d2e47
// last-edited: 2026-10-26

import { describe, it, expect } from 'vitest';
import { listFieldFilters, listSearchText, parseSearch } from './searchParser';

describe('parseSearch: metadata backlog filters', () => {
  it('turns the owner query into two server-side field filters', () => {
    const parsed = parseSearch('-metadata:applied duration:>20m');
    expect(parsed.freeText).toBe('');
    expect(parsed.fieldFilters).toEqual([
      { field: 'metadata', value: 'applied', negated: true, quoted: false },
      { field: 'duration', value: '>20m', negated: false, quoted: false },
    ]);
  });

  it('keeps a bracketed range with spaces as one value', () => {
    const parsed = parseSearch('duration:[10m TO 2h] hush');
    expect(parsed.fieldFilters).toEqual([
      { field: 'duration', value: '[10m TO 2h]', negated: false, quoted: false },
    ]);
    expect(parsed.freeText).toBe('hush');
  });

  it('recognises has_duration', () => {
    expect(parseSearch('has_duration:no').fieldFilters[0]).toMatchObject({
      field: 'has_duration',
      value: 'no',
    });
  });
});

describe('parseSearch: unified grammar values (2026-10-06)', () => {
  it('parses the owner query with a glob title as one field filter', () => {
    const parsed = parseSearch('-metadata:applied -review:no_match -duration:<10m title:a*');
    expect(parsed.freeText).toBe('');
    expect(parsed.fieldFilters.at(-1)).toEqual({
      field: 'title',
      value: 'a*',
      negated: false,
      quoted: false,
    });
  });

  it('keeps a regex with spaces inside the slashes as one value, slashes included', () => {
    const parsed = parseSearch('title:/chapter \\d+ of/ hush');
    expect(parsed.fieldFilters).toEqual([
      { field: 'title', value: '/chapter \\d+ of/', negated: false, quoted: false },
    ]);
    expect(parsed.freeText).toBe('hush');
  });

  it('negates a regex with - and with NOT', () => {
    expect(parseSearch('-title:/^\\s*\\d/').fieldFilters).toEqual([
      { field: 'title', value: '/^\\s*\\d/', negated: true, quoted: false },
    ]);
    expect(parseSearch('NOT title:/^\\s*\\d/').fieldFilters).toEqual([
      { field: 'title', value: '/^\\s*\\d/', negated: true, quoted: false },
    ]);
  });

  it('does not end a regex at an escaped slash', () => {
    expect(parseSearch('title:/a\\/b c/').fieldFilters[0].value).toBe('/a\\/b c/');
  });

  it('keeps an unterminated regex as the value so the error can be shown', () => {
    const parsed = parseSearch('title:/^abc def');
    expect(parsed.fieldFilters[0].value).toBe('/^abc def');
    expect(parsed.freeText).toBe('');
  });

  it('keeps characters glued after the closing slash in the value (rejected, not leaked to free text)', () => {
    const parsed = parseSearch('title:/abc/i');
    expect(parsed.fieldFilters[0].value).toBe('/abc/i');
    expect(parsed.freeText).toBe('');
  });
});

describe('parseSearch: ident and asin (2026-10-11)', () => {
  it('turns ident:missing into a server-side field filter', () => {
    const parsed = parseSearch('ident:missing');
    expect(parsed.freeText).toBe('');
    expect(parsed.fieldFilters).toEqual([
      { field: 'ident', value: 'missing', negated: false, quoted: false },
    ]);
  });

  it('negates ident with - and with NOT, alongside other filters', () => {
    const parsed = parseSearch('-ident:applied NOT ident:held -metadata:applied');
    expect(parsed.fieldFilters).toEqual([
      { field: 'ident', value: 'applied', negated: true, quoted: false },
      { field: 'ident', value: 'held', negated: true, quoted: false },
      { field: 'metadata', value: 'applied', negated: true, quoted: false },
    ]);
  });

  it('recognises asin, keeping the free text separate', () => {
    const parsed = parseSearch('asin:B0SYNTH001 hush');
    expect(parsed.fieldFilters).toEqual([
      { field: 'asin', value: 'B0SYNTH001', negated: false, quoted: false },
    ]);
    expect(parsed.freeText).toBe('hush');
  });

  it('keeps an asin glob as the value', () => {
    expect(parseSearch('asin:B0*').fieldFilters[0]).toMatchObject({ field: 'asin', value: 'B0*' });
  });
});

describe('parseSearch: a per-user term the server must read whole (serverQuery)', () => {
  // Without serverQuery these became two ANDed field filters with the OR left as the
  // free-text word "OR", so the server never saw the OR and the per-user checks
  // never ran on the shape the person typed.
  it.each([
    'read_status:finished OR author:kyle',
    'read_status:finished || author:kyle',
    '(read_status:finished title:Hush)',
    'NOT (read_status:finished title:Hush)',
    '-(read_status:finished title:Hush)',
    'progress_pct:>50 OR title:"Year One"',
    'last_played:7d OR title:Knightfall',
    'Read_Status:finished OR title:Hush',
    'read_status:finished AND title:Hush',
    'read_status:finished && title:Hush',
  ])('%s is sent to the server as typed', (q) => {
    expect(parseSearch(q).serverQuery).toBe(q);
  });

  it.each([
    'read_status:finished',
    'read_status:finished author:kyle',
    'read_status:(finished|in_progress)',
    'read_status:(finished | in_progress) title:Hush',
    'title:"Year (One)" read_status:finished',
    'title:"Year (One)"',
    'title:"read_status:finished" title:Hush',
    'tag:noir',
    'tag:"to-read" title:Hush',
    'tag:"c++*" title:Hush',
    'tag:"noir | gotham"',
  ])('%s keeps the field/value split', (q) => {
    expect(parseSearch(q).serverQuery).toBeUndefined();
  });

  it('keeps a bare NOT as a negated filter, as the server reads it', () => {
    const parsed = parseSearch('NOT read_status:finished title:Hush');
    expect(parsed.serverQuery).toBeUndefined();
    expect(parsed.freeText).toBe('');
    expect(parsed.fieldFilters).toEqual([
      { field: 'read_status', value: 'finished', negated: true, quoted: false },
      { field: 'title', value: 'Hush', negated: false, quoted: false },
    ]);
  });

  it('reads a per-user alternation with spaces as one value', () => {
    const parsed = parseSearch('read_status:(finished | in_progress) title:Hush');
    expect(parsed.fieldFilters).toEqual([
      { field: 'read_status', value: '(finished | in_progress)', negated: false, quoted: false },
      { field: 'title', value: 'Hush', negated: false, quoted: false },
    ]);
    expect(parsed.freeText).toBe('');
  });

  it('leaves an unclosed per-user parenthesis to the server instead of swallowing the rest', () => {
    const parsed = parseSearch('read_status:(finished title:Hush');
    expect(parsed.serverQuery).toBe('read_status:(finished title:Hush');
  });
});

// O1: the flat field/value split mishandles every boolean shape, not only the ones
// that name a per-user field. Each of these used to give a list that looked narrowed
// and was not, with no message (checked on production 2026-10-26): the negation of a
// tag was dropped, an OR became the word "OR" ANDed with both tags, and a pattern
// became a tag of that name. Sent whole, the server's parser answers or refuses it,
// and the Library shows the refusal.
describe('parseSearch: any boolean structure is sent whole (serverQuery)', () => {
  it.each([
    'Batman -tag:"to-read"',
    '-tag:noir',
    'NOT tag:noir',
    'tag:noir OR tag:gotham',
    'tag:noir || tag:gotham',
    'tag:noir*',
    'tag:*noir',
    'tag:noir~',
    'tag:noir~1',
    'tag:no*ir',
    'tag:noir^2',
    'tag:"noir"^2',
    'tag:"noir"~',
    'tag:>noir',
    'tag:[a TO c]',
    'tag:(noir|gotham)',
    'title:"Year One" OR title:Hush',
    'title:(Hush|Knightfall)',
    '(title:Hush title:Knightfall)',
    'title:Hush | title:Knightfall',
    'title:Hush AND author:kyle',
    'title:Hush && author:kyle',
    'title:"read_status:finished" OR title:Hush',
    'read_status:finished -tag:noir',
    '(tag:noir OR title:Hush) author:kyle',
    'author:kyle -(tag:noir title:Hush)',
  ])('%s is sent to the server as typed, with no field filters', (q) => {
    const parsed = parseSearch(q);
    expect(parsed.serverQuery).toBe(q);
    expect(listSearchText(parsed, 'ignored')).toBe(q);
    expect(listFieldFilters(parsed)).toEqual([]);
  });

  it.each([
    'tag:noir',
    'tag:noir tag:gotham',
    'Batman tag:"to-read"',
    'Batman tag:noir author:kyle',
    'tag:"metadata:source:audible"',
    'tag:"c++*"',
    // a negated filter on any other field is carried exactly as a filter, and a field
    // only the filter layer knows (metadata:, library_state:) cannot go in search=
    'Batman -title:Hush',
    'NOT narrator:alfred',
    '-metadata:applied title:Hush',
    'NOT library_state:imported',
    'Batman "Dark (Knight)"',
    'Batman "this OR that"',
  ])('%s keeps the field/value split', (q) => {
    expect(parseSearch(q).serverQuery).toBeUndefined();
  });

  it('still reads a plain tag term as a tag filter that travels as tags[]', () => {
    const parsed = parseSearch('Batman tag:"to-read"');
    expect(parsed.fieldFilters).toEqual([
      { field: 'tag', value: 'to-read', negated: false, quoted: true },
    ]);
    expect(listFieldFilters(parsed)).toEqual([]);
  });
});

describe('listSearchText and listFieldFilters', () => {
  it('sends the raw query as search= and no filters when the server must read it', () => {
    const parsed = parseSearch('read_status:finished OR author:kyle');
    expect(listSearchText(parsed, 'ignored')).toBe('read_status:finished OR author:kyle');
    expect(listFieldFilters(parsed)).toEqual([]);
  });

  it('sends the free text and the parsed filters otherwise', () => {
    const parsed = parseSearch('read_status:finished title:"Year One" tag:noir Hush');
    expect(listSearchText(parsed, 'ignored')).toBe('Hush');
    expect(listFieldFilters(parsed)).toEqual([
      { field: 'read_status', value: 'finished', negated: false },
      { field: 'title', value: 'Year One', negated: false, quoted: true },
    ]);
  });

  it('falls back to the debounced text with no parsed search', () => {
    expect(listSearchText(null, 'Hush')).toBe('Hush');
    expect(listFieldFilters(null)).toEqual([]);
  });
});
