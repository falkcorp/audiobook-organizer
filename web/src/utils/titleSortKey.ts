// file: web/src/utils/titleSortKey.ts
// version: 1.2.0
// guid: 8f2a6d31-4c7e-4b59-a0d3-1e9b5c7f2a64
// last-edited: 2026-09-29

/**
 * The library's title sort key, ported from internal/util/title_sort.go so a
 * table sorted in the browser orders titles exactly as the server does.
 *
 * A numbered-book title ("I Corinthians", "1 Corinthians", "First
 * Corinthians", or "l Corinthians" with the letter l for the digit 1) sorts as
 * "corinthians 1" under the book's name. Every other title is only trimmed and
 * lower-cased: "V for Vendetta", "I, Robot" and "l'Étranger" are untouched.
 * The Go tests and titleSortKey.test.ts share the same case table.
 */

const NUMBERED_BOOKS = [
  'samuel',
  'kings',
  'chronicles',
  'esdras',
  'maccabees',
  'corinthians',
  'thessalonians',
  'timothy',
  'peter',
  'john',
  // German Bible numbering: "1. Mose" … "5. Mose"
  'mose',
];

const ORDINALS: Record<string, number> = {
  '1': 1,
  i: 1,
  first: 1,
  '1st': 1,
  '2': 2,
  ii: 2,
  second: 2,
  '2nd': 2,
  '3': 3,
  iii: 3,
  third: 3,
  '3rd': 3,
  '4': 4,
  iv: 4,
  fourth: 4,
  '4th': 4,
};

// WS is Go RE2's \s: ASCII whitespace only. JavaScript's \s also matches
// NBSP and the other Unicode spaces, which would let the browser rewrite a
// title the server leaves alone.
const WS = '[\\t\\n\\f\\r ]';

// Mirrors numberedBookRe: ordinal token, a separator (a dot, a hyphen or
// underscore run, or whitespace: "1. John", "2 - Kings", "2-Peter"), a
// numbered book name, and a remainder that is empty, starts with a
// non-letter, or is "chapter …".
const NUMBERED_BOOK_RE = new RegExp(
  `^${WS}*([0-9a-z]+)(?:\\.${WS}*|${WS}*[-_]+${WS}*|${WS}+)(${NUMBERED_BOOKS.join('|')})` +
    `((?:${WS}*[^\\p{L}\\t\\n\\f\\r '\u2019].*)|(?:${WS}+(?:chapter|ch\\.?)\\b.*)|${WS}*)$`,
  'iu'
);

// mayBeOrdinal mirrors the Go fast path: the first token (up to a space, tab,
// dot, hyphen or underscore) must be an ordinal or made of l/I/i.
function mayBeOrdinal(title: string): boolean {
  const t = title.replace(/^[ \t]+/, '');
  const end = t.search(/[ \t.\-_]/);
  if (end <= 0) return false;
  const tok = t.slice(0, end).toLowerCase();
  if (tok in ORDINALS) return true;
  return /^[li]+$/.test(tok);
}

function ordinalOf(tok: string): number {
  const lower = tok.toLowerCase();
  if (lower in ORDINALS) return ORDINALS[lower];
  if (tok.includes('l') && /^[lIi]+$/.test(tok)) {
    const roman = lower.replace(/l/g, 'i');
    if (roman in ORDINALS) return ORDINALS[roman];
  }
  return 0;
}

/** "<Book> <n>[ <rest>]", no space before a rest starting with punctuation. */
function joinSortForm(book: string, n: number, rest: string): string {
  if (!rest) return `${book} ${n}`;
  if (':;,.!?)'.includes(rest[0])) return `${book} ${n}${rest}`;
  return `${book} ${n} ${rest}`;
}

/** "<Book> <n>[ <rest>]" for a numbered-book title, or null. */
export function numberedBookSortForm(title: string): string | null {
  if (!mayBeOrdinal(title)) return null;
  const m = NUMBERED_BOOK_RE.exec(title);
  if (!m) return null;
  const n = ordinalOf(m[1]);
  if (n === 0) return null;
  return joinSortForm(m[2], n, m[3].trim());
}

/** The title sort key: numbered books rewritten, then trimmed + lower-cased. */
export function titleSortKey(title: string | null | undefined): string {
  const t = title ?? '';
  return (numberedBookSortForm(t) ?? t).trim().toLowerCase();
}
