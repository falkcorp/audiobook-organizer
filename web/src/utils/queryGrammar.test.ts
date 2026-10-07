// file: web/src/utils/queryGrammar.test.ts
// version: 1.0.0
// guid: 5f9c3a28-1e6d-4b70-8c42-b7e0d4a9f163
// last-edited: 2026-10-06

import { describe, expect, it } from 'vitest';
import {
  compileTitleFilter,
  compileValue,
  firstSearchError,
  re2ToJsRegExp,
  regexValueError,
} from './queryGrammar';
import { parseSearch } from './searchParser';

describe('regexValueError (client pre-check)', () => {
  it.each([
    ['/abc', /no closing \//],
    ['//', /empty regex/],
    ['/abc/i', /after the closing \//],
    ['/^(?!The)/', /no lookahead/],
    ['/(?<=a)b/', /no lookahead/],
    ['/(a)\\1/', /backreferences/],
    ['/a*+/', /possessive/],
  ])('%s is an error', (raw, msg) => {
    expect(regexValueError(raw)).toMatch(msg);
  });

  it.each(['/^\\s*\\p{L}/', '/a\\/b/', '/\\++/', 'plain', '/x/'])('%s is accepted', (raw) => {
    expect(regexValueError(raw)).toBeNull();
  });

  it('ignores quoted values', () => {
    expect(regexValueError('/abc', true)).toBeNull();
  });

  it('firstSearchError names the token like the server 400', () => {
    expect(firstSearchError(parseSearch('author:x -title:/^(?=a)/').fieldFilters)).toMatch(
      /^title:\/\^\(\?=a\)\/ — invalid regex/
    );
    expect(firstSearchError(parseSearch('title:a* -title:/^\\s*\\d/').fieldFilters)).toBeNull();
  });

  it('tag and read_status: patterns and -tag are visible errors, not silent no-ops', () => {
    expect(firstSearchError(parseSearch('-tag:read').fieldFilters)).toMatch(/not supported/);
    expect(firstSearchError(parseSearch('tag:sci*').fieldFilters)).toMatch(/exact tag name/);
    expect(firstSearchError(parseSearch('read_status:/fin/').fieldFilters)).toMatch(/exact status/);
    expect(firstSearchError(parseSearch('tag:scifi read_status:finished').fieldFilters)).toBeNull();
  });
});

describe('re2ToJsRegExp', () => {
  it('compiles \\p{L} with the u flag and is case-insensitive by default', () => {
    const { regex } = re2ToJsRegExp('^\\s*\\p{L}');
    expect(regex?.flags).toContain('u');
    expect(regex?.test('  Émile')).toBe(true);
    expect(regex?.test('01 x')).toBe(false);
    expect(re2ToJsRegExp('^dune$').regex?.test('DUNE')).toBe(true);
  });

  it('translates RE2-only spellings', () => {
    expect(re2ToJsRegExp('\\pL+').regex?.test('abc')).toBe(true);
    expect(re2ToJsRegExp('(?-i)^dune$').regex?.test('DUNE')).toBe(false);
    expect(re2ToJsRegExp('^[[:digit:]]+\\z').regex?.test('123')).toBe(true);
    expect(re2ToJsRegExp('\\Aab').regex?.test('xab')).toBe(false);
    expect(re2ToJsRegExp('\\Qa.b\\E').regex?.test('axb')).toBe(false);
    expect(re2ToJsRegExp('\\Qa.b\\E').regex?.test('a.b')).toBe(true);
    expect(re2ToJsRegExp('(?P<n>a)').regex?.test('a')).toBe(true);
  });

  it('rejects what RE2 rejects instead of silently accepting it', () => {
    expect(re2ToJsRegExp('(?=a)').error).toMatch(/lookahead/);
    expect(re2ToJsRegExp('(a)\\1').error).toMatch(/backreferences/);
  });
});

describe('compileValue', () => {
  it.each([
    ['a*', 'Alpha', true],
    ['a*', '  alpha', true],
    ['a*', 'The Alpha', false],
    ['*saga', 'Hyperion Saga', true],
    ['*', 'x', true],
    ['*', ' ', false],
    ['vamp', 'The VAMPIRE', true],
    ['/^\\s*\\p{L}/', '01 x', false],
  ])('%s on %s = %s', (raw, s, want) => {
    expect(compileValue(raw).test(s)).toBe(want);
  });

  it('quoted is literal', () => {
    expect(compileValue('a*', true).test('Alpha')).toBe(false);
    expect(compileValue('a*', true).test('grade a*')).toBe(true);
  });
});

describe('compileTitleFilter (Review Title box follow-up)', () => {
  it('a blank box passes everything', () => {
    const f = compileTitleFilter('  ');
    expect(f.active).toBe(false);
    expect(f.test('anything')).toBe(true);
  });

  it('a bare value is the title: value grammar', () => {
    expect(compileTitleFilter('/^\\s*\\p{L}/').test('Alpha')).toBe(true);
    expect(compileTitleFilter('/^\\s*\\p{L}/').test('01 Alpha')).toBe(false);
    expect(compileTitleFilter('a*').test('Alpha')).toBe(true);
    // Bare text is a substring now, not a regex: "(" is literal.
    expect(compileTitleFilter('part (1').test('Part (1) of 2')).toBe(true);
  });

  it('accepts title: tokens with negation', () => {
    const f = compileTitleFilter('title:a* -title:/\\d$/');
    expect(f.error).toBeNull();
    expect(f.test('Alpha')).toBe(true);
    expect(f.test('Alpha 2')).toBe(false);
    expect(f.test('Beta')).toBe(false);
  });

  it('an invalid value is a visible error, and matches nothing', () => {
    const f = compileTitleFilter('/^(?!The)/');
    expect(f.error).toMatch(/lookahead/);
    expect(f.test('Alpha')).toBe(false);
  });

  it('other fields are an error', () => {
    expect(compileTitleFilter('author:x').error).toMatch(/only title:/);
  });
});
