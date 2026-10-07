// file: web/src/utils/queryGrammar.ts
// version: 1.0.0
// guid: 8e5b2d17-4c9a-4f36-b1e8-0d7a3c6f9e24
// last-edited: 2026-10-06

/**
 * The ONE search value grammar shared by the Library search bar and the
 * Review → Metadata Title filter (owner decision 2026-10-06). The Go twin is
 * internal/querygrammar; the Library evaluates SERVER-side, so the server is
 * the authority and this module only:
 *
 *  1. pre-checks values for errors that are certain under RE2 (unterminated
 *     or empty regex, flags after the slash, lookaround, backreferences) so
 *     the search bar can show them while the user types; and
 *  2. evaluates the grammar client-side for surfaces that already hold their
 *     full set in the browser (the Review lane), translating RE2 syntax that
 *     JS RegExp spells differently.
 *
 * Value forms:
 *   word          case-insensitive substring
 *   "two words"   literal substring (quotes switch every operator off)
 *   /RE2/         regex, case-insensitive by default, unanchored; (?-i) opts out
 *   a* *a *a*     glob on the WHOLE trimmed value; only * is a wildcard
 *   *             the value is non-empty
 *   >N [a TO b]   numeric fields only (server-side)
 */

import { parseSearch, type FieldFilter } from './searchParser';

/** Index of the first unescaped `/` after position 0, or -1. */
function closingSlash(raw: string): number {
  for (let i = 1; i < raw.length; i++) {
    if (raw[i] === '\\') {
      i++;
      continue;
    }
    if (raw[i] === '/') return i;
  }
  return -1;
}

/** Constructs RE2 rejects but JS RegExp would silently accept. */
const RE2_UNSUPPORTED: Array<{ re: RegExp; why: string }> = [
  {
    re: /\(\?<?[=!]/,
    why: 'RE2 has no lookahead/lookbehind; exclude with a negated filter instead, e.g. -title:/^\\s*\\d/',
  },
  { re: /(^|[^\\])(\\\\)*\\[1-9]/, why: 'RE2 has no backreferences (\\1)' },
  { re: /(^|[^\\])(\\\\)*\\k</, why: 'RE2 has no named backreferences (\\k<name>)' },
  { re: /\(\?>/, why: 'RE2 has no atomic groups (?>…)' },
  { re: /(^|[^\\])(\\\\)*[*+?}]\+/, why: 'RE2 has no possessive quantifiers (*+, ++, ?+)' },
];

/**
 * Pre-checks a /regex/ value. Returns an error message, or null when the
 * value is not a regex or has no error this check can be certain of.
 */
export function regexValueError(raw: string, quoted = false): string | null {
  if (quoted || !raw.startsWith('/')) return null;
  const close = closingSlash(raw);
  if (close < 0) {
    return `regex ${raw} has no closing /; close it (e.g. /^\\s*\\p{L}/) or quote the value to search for a literal slash`;
  }
  if (close !== raw.length - 1) {
    return `unexpected "${raw.slice(close + 1)}" after the closing / of regex ${raw.slice(0, close + 1)}; flags are not supported (regex is case-insensitive by default, use (?-i) to opt out)`;
  }
  const pattern = raw.slice(1, close);
  if (!pattern) return 'empty regex //';
  for (const { re, why } of RE2_UNSUPPORTED) {
    if (re.test(pattern)) return `invalid regex ${raw}: ${why}`;
  }
  return null;
}

/**
 * The first certain error in a parsed search, worded like the server's 400
 * (`field:value — reason`), or null.
 */
export function firstSearchError(filters: readonly FieldFilter[]): string | null {
  for (const f of filters) {
    // tag: is an exact tag lookup done by tag name (not a field filter), and
    // only the first positive one is used. Say so rather than ignore it.
    if (f.field === 'tag') {
      if (f.negated) return `-tag:${f.value} — excluding a tag is not supported yet`;
      if (!f.quoted && (f.value.includes('*') || f.value.startsWith('/'))) {
        return `tag:${f.value} — tag takes an exact tag name, not a wildcard or regex`;
      }
      continue;
    }
    if (f.field === 'read_status' && !f.quoted && (f.value.includes('*') || f.value.startsWith('/'))) {
      return `read_status:${f.value} — read_status takes an exact status, not a pattern`;
    }
    const err = regexValueError(f.value, f.quoted);
    if (err) return `${f.field}:${f.value} — ${err}`;
  }
  return null;
}

const POSIX_CLASSES: Record<string, string> = {
  alnum: '0-9A-Za-z',
  alpha: 'A-Za-z',
  ascii: '\\x00-\\x7F',
  blank: '\\t ',
  cntrl: '\\x00-\\x1F\\x7F',
  digit: '0-9',
  graph: '!-~',
  lower: 'a-z',
  print: ' -~',
  punct: '!-\\/:-@\\[-`{-~',
  space: '\\t\\n\\v\\f\\r ',
  upper: 'A-Z',
  word: '0-9A-Za-z_',
  xdigit: '0-9A-Fa-f',
};

// No `-`: escaping it outside a class is a SyntaxError under the `u` flag.
function escapeLiteral(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&');
}

/**
 * Translates an RE2 pattern to a JS RegExp compiled with the `u` flag (needed
 * for \p{L}) and `i` unless a leading (?-i) opts out. Returns an error string
 * for anything RE2 rejects or this translator cannot express — never a
 * pattern that silently behaves differently.
 */
export function re2ToJsRegExp(pattern: string): { regex: RegExp | null; error: string | null } {
  for (const { re, why } of RE2_UNSUPPORTED) {
    if (re.test(pattern)) return { regex: null, error: why };
  }
  const flags = new Set(['i', 'u']);
  let src = pattern;
  // A leading inline flag group, e.g. (?i) (?s) (?-i) (?is). Scoped or
  // mid-pattern flag groups are rejected below (JS support is too new).
  const lead = /^\(\?([imsU]*)(?:-([imsU]*))?\)/.exec(src);
  if (lead) {
    for (const f of lead[1] ?? '') flags.add(f);
    for (const f of lead[2] ?? '') flags.delete(f);
    if (flags.has('U')) return { regex: null, error: 'the (?U) ungreedy flag is not supported here' };
    src = src.slice(lead[0].length);
  }
  let out = '';
  let inClass = false;
  for (let i = 0; i < src.length; i++) {
    const ch = src[i];
    if (ch === '\\') {
      const nx = src[i + 1];
      if (nx === undefined) return { regex: null, error: 'trailing backslash' };
      if (nx === 'Q') {
        const end = src.indexOf('\\E', i + 2);
        const lit = end < 0 ? src.slice(i + 2) : src.slice(i + 2, end);
        out += escapeLiteral(lit);
        i = end < 0 ? src.length : end + 1;
        continue;
      }
      if ((nx === 'p' || nx === 'P') && src[i + 2] !== '{' && src[i + 2] !== undefined) {
        out += `\\${nx}{${src[i + 2]}}`;
        i += 2;
        continue;
      }
      if (!inClass && nx === 'z') {
        out += '$';
        i++;
        continue;
      }
      if (!inClass && nx === 'A') {
        out += '^';
        i++;
        continue;
      }
      out += ch + nx;
      i++;
      continue;
    }
    if (inClass) {
      if (ch === '[' && src[i + 1] === ':') {
        const end = src.indexOf(':]', i + 2);
        const name = end < 0 ? '' : src.slice(i + 2, end);
        if (name.startsWith('^')) {
          return { regex: null, error: `negated POSIX class [:${name}:] is not supported here` };
        }
        const cls = POSIX_CLASSES[name];
        if (!cls) return { regex: null, error: `unknown POSIX class [:${name}:]` };
        out += cls;
        i = end + 1;
        continue;
      }
      if (ch === ']') inClass = false;
      out += ch;
      continue;
    }
    if (ch === '[') {
      inClass = true;
      out += ch;
      if (src[i + 1] === '^') {
        out += '^';
        i++;
      }
      if (src[i + 1] === ']') {
        out += '\\]';
        i++;
      }
      continue;
    }
    if (ch === '(' && src[i + 1] === '?') {
      if (src.startsWith('(?P<', i)) {
        out += '(?<';
        i += 3;
        continue;
      }
      if (!src.startsWith('(?:', i) && !src.startsWith('(?<', i)) {
        return { regex: null, error: 'inline flag groups are only supported at the start, e.g. (?-i)' };
      }
    }
    out += ch;
  }
  try {
    return { regex: new RegExp(out, [...flags].filter((f) => f !== 'U').join('')), error: null };
  } catch (e) {
    return { regex: null, error: e instanceof Error ? e.message : String(e) };
  }
}

export interface ValueMatcher {
  test: (s: string) => boolean;
  error: string | null;
}

const MATCH_NOTHING = () => false;

/** Compiles one text value (not numeric forms) for client-side evaluation. */
export function compileValue(raw: string, quoted = false): ValueMatcher {
  if (!raw) return { test: MATCH_NOTHING, error: 'empty value' };
  if (quoted) {
    const needle = raw.toLowerCase();
    return { test: (s) => s.toLowerCase().includes(needle), error: null };
  }
  if (raw.startsWith('/')) {
    const pre = regexValueError(raw);
    if (pre) return { test: MATCH_NOTHING, error: pre };
    const { regex, error } = re2ToJsRegExp(raw.slice(1, -1));
    if (!regex) return { test: MATCH_NOTHING, error: `invalid regex ${raw}: ${error}` };
    return { test: (s) => regex.test(s), error: null };
  }
  if (raw.includes('*')) {
    if (raw.replace(/\*/g, '') === '') return { test: (s) => s.trim() !== '', error: null };
    const re = new RegExp(`^${raw.split('*').map(escapeLiteral).join('.*')}$`, 'is');
    return { test: (s) => re.test(s.trim()), error: null };
  }
  const needle = raw.toLowerCase();
  return { test: (s) => s.toLowerCase().includes(needle), error: null };
}

export interface TitleFilter extends ValueMatcher {
  /** False when the input is blank (the filter passes every row). */
  active: boolean;
}

/**
 * Compiles a Review Title filter box. It accepts exactly what follows
 * `title:` in the Library — a bare value (`foo`, `a*`, `/re/`, `"x y"`) —
 * or one or more `title:` tokens, any of them negated
 * (`title:a* -title:/^\s*\d/`). Any other field is an error.
 */
export function compileTitleFilter(input: string): TitleFilter {
  const trimmed = input.trim();
  if (!trimmed) return { test: () => true, error: null, active: false };

  const parsed = parseSearch(trimmed);
  let filters: FieldFilter[];
  if (parsed.fieldFilters.length === 0) {
    const quoted = trimmed.length >= 2 && trimmed.startsWith('"') && trimmed.endsWith('"');
    filters = [
      { field: 'title', value: quoted ? trimmed.slice(1, -1) : trimmed, negated: false, quoted },
    ];
  } else {
    const other = parsed.fieldFilters.find((f) => f.field !== 'title');
    if (other) {
      return {
        test: MATCH_NOTHING,
        error: `${other.field}: only title: filters apply here`,
        active: true,
      };
    }
    if (parsed.freeText) {
      return {
        test: MATCH_NOTHING,
        error: `"${parsed.freeText}": mix of title: tokens and bare text — write every part as title:…`,
        active: true,
      };
    }
    filters = parsed.fieldFilters;
  }
  const compiled = filters.map((f) => ({ f, m: compileValue(f.value, f.quoted) }));
  const bad = compiled.find((c) => c.m.error);
  if (bad) {
    return { test: MATCH_NOTHING, error: `title:${bad.f.value} — ${bad.m.error}`, active: true };
  }
  return {
    test: (s) => compiled.every(({ f, m }) => m.test(s) !== f.negated),
    error: null,
    active: true,
  };
}
