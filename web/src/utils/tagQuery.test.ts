// file: web/src/utils/tagQuery.test.ts
// version: 1.1.0
// guid: 7a3c9e1f-4b6d-4e82-9f05-1d8b3e6a2c97
// last-edited: 2026-10-26

import { describe, expect, it } from 'vitest';
import { appendTagTerm, removeTagTerm, tagTerm, tagTermValues } from './tagQuery';
import { parseSearch } from './searchParser';

describe('tagQuery', () => {
  it('appends a quoted term the parser reads back as the tag (colons included)', () => {
    const q = appendTagTerm('dune', 'metadata:language:en');
    expect(q).toBe('dune tag:"metadata:language:en"');
    const parsed = parseSearch(q);
    expect(parsed.freeText).toBe('dune');
    expect(parsed.fieldFilters).toEqual([
      { field: 'tag', value: 'metadata:language:en', negated: false, quoted: true },
    ]);
  });

  it('appends to an empty query and does not duplicate a present term', () => {
    expect(appendTagTerm('', 'fantasy')).toBe(tagTerm('fantasy'));
    expect(appendTagTerm('x tag:"fantasy"', 'fantasy')).toBe('x tag:"fantasy"');
    expect(appendTagTerm('x tag:fantasy', 'fantasy')).toBe('x tag:fantasy');
  });

  it('removes quoted and bare terms but leaves negated ones and prefixes alone', () => {
    expect(removeTagTerm('dune tag:"fantasy" author:herbert', 'fantasy')).toBe(
      'dune author:herbert'
    );
    expect(removeTagTerm('tag:fantasy', 'fantasy')).toBe('');
    expect(removeTagTerm('-tag:fantasy tag:fantasy-epic', 'fantasy')).toBe(
      '-tag:fantasy tag:fantasy-epic'
    );
    expect(removeTagTerm('NOT tag:fantasy', 'fantasy')).toBe('NOT tag:fantasy');
  });

  it('finds and removes a term inside a group, and drops a group it empties', () => {
    expect(removeTagTerm('(tag:"noir")', 'noir')).toBe('');
    expect(removeTagTerm('x (tag:noir)', 'noir')).toBe('x');
    expect(removeTagTerm('(a:b tag:noir) c:d', 'noir')).toBe('(a:b) c:d');
    expect(appendTagTerm('(a:b tag:noir)', 'noir')).toBe('(a:b tag:noir)');
  });

  it('reads tag terms the way the server grammar does, groups and quotes included', () => {
    expect(tagTermValues('read_status:finished AND tag:"noir"')).toEqual(['noir']);
    expect(tagTermValues('(read_status:finished tag:noir)')).toEqual(['noir']);
    expect(tagTermValues('(tag:noir read_status:finished) tag:"dark knight"')).toEqual([
      'noir',
      'dark knight',
    ]);
    // negated, glued to a word, or inside another term's quotes: not a term
    expect(tagTermValues('-tag:noir NOT tag:gotham xtag:camelot title:"a tag:b"')).toEqual([]);
    expect(tagTermValues('tag:"unterminated')).toEqual([]);
  });
});
