// file: web/src/utils/tagQuery.test.ts
// version: 1.2.1
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

  it('folds a tag the way the server does, so a chip for "noir" is on for tag:Noir', () => {
    expect(tagTermValues('tag:Noir')).toEqual(['noir']);
    expect(tagTermValues('tag:" Dark-Knight "')).toEqual(['dark-knight']);
    expect(removeTagTerm('a:b tag:Noir', 'noir')).toBe('a:b');
    expect(appendTagTerm('a:b tag:Noir', 'noir')).toBe('a:b tag:Noir');
  });

  // R-B1 / NIT 2: only a term that narrows the whole query is "active" (the server
  // refuses the others). A chip shows on for an active term and for nothing else.
  it('lists only the tag terms that narrow the whole query', () => {
    for (const q of [
      'read_status:finished -(tag:"noir")',
      'read_status:finished NOT (tag:noir author:kyle)',
      '-(a:b (tag:noir))',
      '(tag:noir OR a:b)',
      'tag:noir OR a:b',
      'a:b || tag:noir',
      'tag:(noir|gotham)',
      'tag:noir* tag:noir~ tag:>a',
      '-tag:noir',
    ]) {
      expect(tagTermValues(q), q).toEqual([]);
    }
    // an OR inside a child group does not reach the parent's terms
    expect(tagTermValues('a:b (tag:noir OR c:d) tag:gotham')).toEqual(['gotham']);
    expect(tagTermValues('tag:noir (a:b OR c:d)')).toEqual(['noir']);
    expect(tagTermValues('x (y (tag:noir))')).toEqual(['noir']);
  });

  // NIT 1: a removed term takes its connective with it, so the box never holds text
  // the server cannot read ("a AND", "AND b", "( OR b)").
  it('removes a term without leaving a dangling AND or OR', () => {
    for (const [q, want] of [
      ['read_status:finished AND tag:"noir"', 'read_status:finished'],
      ['tag:"noir" AND read_status:finished', 'read_status:finished'],
      ['a:b AND tag:noir AND c:d', 'a:b AND c:d'],
      ['a:b tag:noir AND c:d', 'a:b AND c:d'],
      ['a:b && tag:noir', 'a:b'],
      ['tag:noir && a:b', 'a:b'],
      ['read_status:finished AND (tag:noir)', 'read_status:finished'],
      ['a:b AND (c:d tag:noir)', 'a:b AND (c:d)'],
      ['(tag:noir AND c:d) AND e:f', '(c:d) AND e:f'],
      ['tag:noir', ''],
    ] as const) {
      expect(removeTagTerm(q, 'noir'), q).toBe(want);
    }
  });

  it('leaves a term that is not active alone, so a click does not delete an exclusion', () => {
    for (const q of [
      'a:b -(tag:"noir")',
      'a:b AND NOT (tag:noir c:d)',
      '(tag:noir OR c:d)',
      'a:b -tag:noir',
      'a:b AND -tag:noir',
    ]) {
      expect(removeTagTerm(q, 'noir'), q).toBe(q);
    }
    // and a second, active term is added beside an excluded one
    expect(appendTagTerm('a:b -(tag:"noir")', 'noir')).toBe('a:b -(tag:"noir") tag:"noir"');
  });

  it('wraps a query that has an OR on its top level before adding a tag', () => {
    expect(appendTagTerm('a:b OR c:d', 'noir')).toBe('(a:b OR c:d) tag:"noir"');
    expect(appendTagTerm('a:b (c:d OR e:f)', 'noir')).toBe('a:b (c:d OR e:f) tag:"noir"');
    expect(appendTagTerm('a:b AND c:d', 'noir')).toBe('a:b AND c:d tag:"noir"');
  });

  // N4: the text the server's parser refuses or reads differently must not read as
  // an active chip here.
  it('reads a tag with a boost, fuzzy marker or pattern as refused: no chip is on for it', () => {
    for (const q of [
      'tag:"noir"^2',
      'tag:"noir"~',
      'tag:"noir"~2',
      'tag:"noir" ^2',
      'a:b tag:"noir"^1.5',
      'tag:noir*',
      'tag:*noir',
      'tag:no*ir',
      'tag:noir~1',
      'tag:noir^2',
      'tag:x^',
    ]) {
      expect(tagTermValues(q), q).toEqual([]);
    }
    // quoted, the same characters are part of the name
    expect(tagTermValues('tag:"c++*"')).toEqual(['c++*']);
    expect(tagTermValues('tag:"no*ir"')).toEqual(['no*ir']);
    // a modifier-looking word that does not follow a tag term is not the tag's
    expect(tagTermValues('tag:"noir" a:b ^2')).toEqual(['noir']);
  });

  it('does not delete a refused term when its chip is clicked off', () => {
    for (const q of ['tag:"noir"^2', 'tag:noir*', 'tag:"noir" ^2']) {
      expect(removeTagTerm(q, 'noir'), q).toBe(q);
    }
  });

  it('drops a trailing NOT, -, AND or OR before appending, so the new tag is not its operand', () => {
    expect(appendTagTerm('a:b NOT', 'noir')).toBe('a:b tag:"noir"');
    expect(appendTagTerm('a:b -', 'noir')).toBe('a:b tag:"noir"');
    expect(appendTagTerm('a:b AND', 'noir')).toBe('a:b tag:"noir"');
    expect(appendTagTerm('a:b OR', 'noir')).toBe('a:b tag:"noir"');
    expect(appendTagTerm('a:b NOT AND', 'noir')).toBe('a:b tag:"noir"');
    expect(appendTagTerm('NOT', 'noir')).toBe('tag:"noir"');
    expect(appendTagTerm('-', 'noir')).toBe('tag:"noir"');
    // the dangling operator does not hide that the tag is already there
    expect(appendTagTerm('tag:"noir" NOT', 'noir')).toBe('tag:"noir" NOT');
    // and the appended term is active: nothing in front of it negates it
    expect(tagTermValues(appendTagTerm('a:b NOT', 'noir'))).toEqual(['noir']);
    expect(tagTermValues(appendTagTerm('a:b -', 'noir'))).toEqual(['noir']);
  });

  it('removes the NOT or `-` that was left with nothing to apply to', () => {
    // A `-` with a space after it is a word of its own, not a negation of the term
    // (the server drops it as noise), and a NOT before an AND applies to nothing.
    for (const [q, want] of [
      ['- tag:x', ''],
      ['NOT AND tag:x', ''],
      ['a:b - tag:x', 'a:b'],
      ['a:b AND - tag:x', 'a:b'],
      ['tag:x -', ''],
    ] as const) {
      expect(removeTagTerm(q, 'x'), q).toBe(want);
    }
    // NOT or -tag:x directly in front of the term negate it: an exclusion, left alone
    for (const q of ['a:b NOT tag:x', 'a:b -tag:x', '(NOT tag:x) a:b']) {
      expect(removeTagTerm(q, 'x'), q).toBe(q);
    }
  });
});
