// file: web/src/utils/tagQuery.test.ts
// version: 1.0.0
// guid: 7a3c9e1f-4b6d-4e82-9f05-1d8b3e6a2c97
// last-edited: 2026-10-06

import { describe, expect, it } from 'vitest';
import { appendTagTerm, removeTagTerm, tagTerm } from './tagQuery';
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
});
