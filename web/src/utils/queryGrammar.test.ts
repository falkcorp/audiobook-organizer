// file: web/src/utils/queryGrammar.test.ts
// version: 1.3.0
// guid: 5f9c3a28-1e6d-4b70-8c42-b7e0d4a9f163
// last-edited: 2026-10-18

import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  compileTitleFilter,
  compileValue,
  dateTermError,
  dateValueError,
  firstSearchError,
  isDateField,
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

describe('relative dates (client pre-check)', () => {
  it('knows which fields take a date', () => {
    for (const f of ['last_played', 'date_added', 'added', 'date_modified', 'modified']) {
      expect(isDateField(f)).toBe(true);
    }
    // itunes_last_played is sort-only on the server, not a filter field.
    for (const f of ['itunes_last_played', 'year', 'title', 'created_at']) {
      expect(isDateField(f)).toBe(false);
    }
  });

  it.each([
    'now',
    'NOW',
    '2026-01-31',
    '2024-02-29',
    '2026-03-04T10:20:30Z',
    '2026-03-04T10:20:30.5+02:00',
    '-30d',
    '+2w',
    '-1m',
    '-1Y',
    '-100000d',
    '-0d',
  ])('%s is a valid date term', (v) => {
    expect(dateTermError(v)).toBeNull();
  });

  it('an unsigned offset is refused with the signed spelling in the message', () => {
    expect(dateTermError('30d')).toBe(
      'date offset "30d" needs a sign: write -30d for 30 days ago, or +30d for 30 days from now'
    );
    expect(dateTermError('1y')).toMatch(/write -1y for 1 year ago, or \+1y for 1 year from now/);
  });

  it.each([
    ['2026-02-31', /that day does not exist/],
    ['2025-02-29', /that day does not exist/],
    ['2026-13-01', /that day does not exist/],
    ['yesterday', /use YYYY-MM-DD, now, or an offset like -30d/],
    ['-30x', /use YYYY-MM-DD, now, or an offset like -30d/],
    ['-d', /use YYYY-MM-DD/],
    ['-100001d', /too large/],
    ['', /empty date/],
  ])('%j is refused', (v, msg) => {
    expect(dateTermError(v)).toMatch(msg);
  });

  it('a value takes an operator, a range, or last_played:*', () => {
    expect(dateValueError('last_played', '>-30d')).toBeNull();
    expect(dateValueError('date_added', '>=2026-01-01')).toBeNull();
    expect(dateValueError('date_added', '<=-1y')).toBeNull();
    expect(dateValueError('last_played', '-30d..now')).toBeNull();
    expect(dateValueError('last_played', '2026-01-01..2026-03-31')).toBeNull();
    expect(dateValueError('last_played', '*')).toBeNull();
    // Only last_played has an "ever" form; the server has no * for the indexed dates.
    expect(dateValueError('date_added', '*')).toMatch(/use YYYY-MM-DD/);
    // Either side of a range is checked.
    expect(dateValueError('last_played', '30d..now')).toMatch(/needs a sign/);
    expect(dateValueError('last_played', '-30d..tomorrow')).toMatch(/invalid date "tomorrow"/);
    // A range has two sides: an open end is not a range.
    expect(dateValueError('last_played', '-30d..')).toMatch(/invalid date/);
    expect(dateValueError('last_played', '..now')).toMatch(/invalid date/);
    // An operator and a range together are not supported.
    expect(dateValueError('last_played', '>-30d..now')).toMatch(/invalid date/);
  });

  it('firstSearchError names the date token, and skips quoted values', () => {
    expect(firstSearchError(parseSearch('last_played:>-30d date_added:-1y..now').fieldFilters)).toBeNull();
    expect(firstSearchError(parseSearch('added:>30d').fieldFilters)).toMatch(
      /^added:>30d — date offset "30d" needs a sign: write -30d/
    );
    expect(firstSearchError(parseSearch('modified:2026-02-31').fieldFilters)).toMatch(
      /^modified:2026-02-31 — invalid date/
    );
    expect(firstSearchError(parseSearch('last_played:"30d"').fieldFilters)).toBeNull();
  });

  it('a date field value is not run through the regex pre-check', () => {
    // A leading slash is a regex on a text field but just an invalid date here.
    expect(firstSearchError(parseSearch('title:/abc').fieldFilters)).toMatch(/no closing/);
    expect(firstSearchError(parseSearch('last_played:/abc').fieldFilters)).toMatch(/invalid date/);
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

interface ConformanceCase {
  name: string;
  pattern: string;
  quoted: boolean;
  input?: string;
  want_match?: boolean;
  want_error?: boolean;
  go_error_contains?: string;
  ts_error_matches?: string;
  engines?: Array<'go' | 'ts'>;
  skip_reason?: string;
}

// The corpus internal/querygrammar's TestConformanceCorpus also reads: one
// file, so the RE2-to-JS translation cannot drift from the Go engine silently.
const corpus: ConformanceCase[] = JSON.parse(
  fs.readFileSync(
    path.resolve(__dirname, '../../../internal/querygrammar/testdata/conformance.json'),
    'utf8'
  )
);
// The same list lives in internal/querygrammar/querygrammar_test.go
// (conformanceEngines).
const CONFORMANCE_ENGINES: ReadonlyArray<string> = ['go', 'ts'];

// The row-shape rules both suites share, so a malformed row fails in both
// rather than running in one and skipping in the other: exactly one of
// want_match / want_error (and never null); engines absent or a non-empty
// subset of CONFORMANCE_ENGINES with no unknown names; skip_reason required
// only when engines actually excludes one. Returns the first violation.
function validateCase(c: ConformanceCase): string | null {
  const raw = c as { want_match?: unknown };
  if ('want_match' in raw && raw.want_match === null) {
    return 'want_match must be true or false, not null';
  }
  if ((c.want_match === undefined) === !c.want_error) {
    return 'a case sets exactly one of want_match and want_error';
  }
  if (c.engines === undefined) return null;
  if (c.engines.length === 0) {
    return 'engines must be absent or a non-empty subset of go,ts';
  }
  for (const e of c.engines) {
    if (!CONFORMANCE_ENGINES.includes(e)) {
      return `unknown engine "${e}" (want one of ${CONFORMANCE_ENGINES.join(',')})`;
    }
  }
  if (c.engines.length < CONFORMANCE_ENGINES.length && !c.skip_reason) {
    return 'a case that excludes an engine must carry a skip_reason';
  }
  return null;
}

const runsOnTs = (c: ConformanceCase) => c.engines === undefined || c.engines.includes('ts');

// Locks the row-shape rules shared with the Go suite
// (TestConformanceCase_Validate): the same inputs must be accepted or
// rejected on both sides.
describe('conformance corpus row shape', () => {
  const row = (extra: Record<string, unknown>): ConformanceCase =>
    ({ name: 'x', pattern: 'a', quoted: false, ...extra }) as ConformanceCase;
  const cases: Array<[string, Record<string, unknown>, string | null]> = [
    ['match row', { want_match: true }, null],
    ['error row', { want_error: true }, null],
    ['explicit both engines, no skip_reason', { want_match: true, engines: ['go', 'ts'] }, null],
    [
      'go only with skip_reason',
      { want_match: true, engines: ['go'], skip_reason: 'ts differs' },
      null,
    ],
    ['want_match null', { want_match: null }, 'not null'],
    ['neither expectation', {}, 'exactly one of want_match and want_error'],
    [
      'both expectations',
      { want_match: false, want_error: true },
      'exactly one of want_match and want_error',
    ],
    ['empty engines', { want_match: true, engines: [] }, 'non-empty subset'],
    [
      'unknown engine',
      { want_match: true, engines: ['golang'], skip_reason: 'r' },
      'unknown engine',
    ],
    [
      'excludes an engine, no skip_reason',
      { want_match: true, engines: ['go'] },
      'must carry a skip_reason',
    ],
  ];
  for (const [name, extra, want] of cases) {
    it(name, () => {
      const got = validateCase(row(extra));
      if (want === null) expect(got).toBeNull();
      else expect(got).toContain(want);
    });
  }
  it('engines [go] runs on go and not on ts', () => {
    expect(runsOnTs(row({ want_match: true, engines: ['go'], skip_reason: 'r' }))).toBe(false);
    expect(runsOnTs(row({ want_match: true }))).toBe(true);
  });
});

describe('conformance corpus', () => {
  const skipped = corpus.filter((c) => !runsOnTs(c)).length;
  console.info(
    `conformance corpus (ts): ${corpus.length} cases, ${corpus.length - skipped} run, ${skipped} skipped`
  );

  for (const c of corpus) {
    it(c.name, (ctx) => {
      expect(validateCase(c)).toBeNull();
      if (!runsOnTs(c)) ctx.skip(c.skip_reason);
      const m = compileValue(c.pattern, c.quoted);
      if (c.want_error) {
        expect(c.ts_error_matches).toBeTruthy();
        expect(m.error).toMatch(new RegExp(c.ts_error_matches as string));
        return;
      }
      expect(m.error).toBeNull();
      expect(m.test(c.input ?? '')).toBe(c.want_match);
    });
  }
});
