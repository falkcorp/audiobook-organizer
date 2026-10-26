// file: web/src/utils/__tests__/searchParser.test.ts
// version: 1.0.1
// guid: 48EF2E2E-4660-4C1C-A6C1-584C4A4B6B63
// last-edited: 2026-10-26

import { describe, it, expect } from 'vitest';
import { parseSearch, SEARCH_FIELDS } from '../searchParser';

describe('SEARCH_FIELDS', () => {
  it('contains known field names', () => {
    expect(SEARCH_FIELDS).toContain('author');
    expect(SEARCH_FIELDS).toContain('title');
    expect(SEARCH_FIELDS).toContain('narrator');
    expect(SEARCH_FIELDS).toContain('series');
    expect(SEARCH_FIELDS).toContain('tag');
    expect(SEARCH_FIELDS).toContain('genre');
    expect(SEARCH_FIELDS).toContain('year');
    expect(SEARCH_FIELDS).toContain('publisher');
    expect(SEARCH_FIELDS).toContain('isbn10');
    expect(SEARCH_FIELDS).toContain('isbn13');
  });

  it('does not contain unknown fields', () => {
    expect(SEARCH_FIELDS).not.toContain('foo');
    expect(SEARCH_FIELDS).not.toContain('bar');
  });
});

describe('parseSearch', () => {
  it('handles plain text with no colons', () => {
    const result = parseSearch('hello world');
    expect(result).toEqual({
      freeText: 'hello world',
      fieldFilters: [],
    });
  });

  it('handles single field filter', () => {
    const result = parseSearch('author:smith');
    expect(result).toEqual({
      freeText: '',
      fieldFilters: [
        { field: 'author', value: 'smith', negated: false, quoted: false },
      ],
    });
  });

  it('handles quoted value', () => {
    const result = parseSearch('author:"Joshua Dalzelle"');
    expect(result).toEqual({
      freeText: '',
      fieldFilters: [
        {
          field: 'author',
          value: 'Joshua Dalzelle',
          negated: false,
          quoted: true,
        },
      ],
    });
  });

  it('handles multiple field filters', () => {
    const result = parseSearch('tag:litrpg author:guy');
    expect(result.freeText).toBe('');
    expect(result.fieldFilters).toHaveLength(2);
    expect(result.fieldFilters[0]).toEqual({
      field: 'tag',
      value: 'litrpg',
      negated: false,
      quoted: false,
    });
    expect(result.fieldFilters[1]).toEqual({
      field: 'author',
      value: 'guy',
      negated: false,
      quoted: false,
    });
  });

  it('handles NOT prefix negation', () => {
    const result = parseSearch('NOT narrator:heitsch');
    expect(result).toEqual({
      freeText: '',
      fieldFilters: [
        { field: 'narrator', value: 'heitsch', negated: true, quoted: false },
      ],
    });
  });

  it('handles dash negation', () => {
    const result = parseSearch('-tag:romance');
    expect(result).toEqual({
      freeText: '',
      fieldFilters: [
        { field: 'tag', value: 'romance', negated: true, quoted: false },
      ],
      // a negated tag cannot be a filter the server applies (a tag filter is exact and
      // only ANDed), so the whole text goes to the server, which says so with a 400
      serverQuery: '-tag:romance',
    });
  });

  it('handles mixed free text and field filters', () => {
    const result = parseSearch('great books author:smith');
    expect(result.freeText).toBe('great books');
    expect(result.fieldFilters).toHaveLength(1);
    expect(result.fieldFilters[0]).toEqual({
      field: 'author',
      value: 'smith',
      negated: false,
      quoted: false,
    });
  });

  it('treats unknown field as free text', () => {
    const result = parseSearch('foo:bar');
    expect(result).toEqual({
      freeText: 'foo:bar',
      fieldFilters: [],
    });
  });

  it('handles empty string', () => {
    const result = parseSearch('');
    expect(result).toEqual({
      freeText: '',
      fieldFilters: [],
    });
  });

  it('handles complex query with multiple fields, negation, and free text', () => {
    const result = parseSearch(
      'author:"Joshua Dalzelle" tag:scifi NOT narrator:heitsch space marine'
    );
    expect(result.freeText).toBe('space marine');
    expect(result.fieldFilters).toHaveLength(3);
    expect(result.fieldFilters[0]).toEqual({
      field: 'author',
      value: 'Joshua Dalzelle',
      negated: false,
      quoted: true,
    });
    expect(result.fieldFilters[1]).toEqual({
      field: 'tag',
      value: 'scifi',
      negated: false,
      quoted: false,
    });
    expect(result.fieldFilters[2]).toEqual({
      field: 'narrator',
      value: 'heitsch',
      negated: true,
      quoted: false,
    });
  });

  it('handles free text before and after field filters', () => {
    const result = parseSearch('hello author:smith world');
    expect(result.freeText).toBe('hello world');
    expect(result.fieldFilters).toHaveLength(1);
    expect(result.fieldFilters[0].field).toBe('author');
    expect(result.fieldFilters[0].value).toBe('smith');
  });

  it('handles NOT before unknown field as free text', () => {
    const result = parseSearch('NOT foo:bar');
    expect(result.freeText).toBe('NOT foo:bar');
    expect(result.fieldFilters).toHaveLength(0);
  });

  it('handles dash before unknown field as free text', () => {
    const result = parseSearch('-foo:bar');
    expect(result.freeText).toBe('-foo:bar');
    expect(result.fieldFilters).toHaveLength(0);
  });

  it('handles multiple quoted values', () => {
    const result = parseSearch(
      'author:"John Smith" narrator:"Jane Doe"'
    );
    expect(result.freeText).toBe('');
    expect(result.fieldFilters).toHaveLength(2);
    expect(result.fieldFilters[0].value).toBe('John Smith');
    expect(result.fieldFilters[1].value).toBe('Jane Doe');
  });

  it('handles field with series_number (underscore field)', () => {
    const result = parseSearch('series_number:5');
    expect(result.fieldFilters).toHaveLength(1);
    expect(result.fieldFilters[0]).toEqual({
      field: 'series_number',
      value: '5',
      negated: false,
      quoted: false,
    });
  });

  it('trims extra whitespace in free text', () => {
    const result = parseSearch('  hello   author:smith   world  ');
    expect(result.freeText).toBe('hello world');
  });
});
