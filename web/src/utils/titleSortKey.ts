// file: web/src/utils/titleSortKey.ts
// version: 1.0.0
// guid: 8f2a6d31-4c7e-4b59-a0d3-1e9b5c7f2a64
// last-edited: 2026-09-28

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

// Mirrors numberedBookRe: ordinal token, optional dot, a numbered book name,
// and a remainder that is empty, starts with a non-letter, or is "chapter …".
const NUMBERED_BOOK_RE = new RegExp(
  `^\\s*([0-9a-z]+)\\.?\\s+(${NUMBERED_BOOKS.join('|')})((?:\\s*[^\\p{L}\\s].*)|(?:\\s+(?:chapter|ch\\.?)\\b.*)|\\s*)$`,
  'iu'
);

function ordinalOf(tok: string): number {
  const lower = tok.toLowerCase();
  if (lower in ORDINALS) return ORDINALS[lower];
  if (tok.includes('l') && /^[lIi]+$/.test(tok)) {
    const roman = lower.replace(/l/g, 'i');
    if (roman in ORDINALS) return ORDINALS[roman];
  }
  return 0;
}

/** "<Book> <n>[ <rest>]" for a numbered-book title, or null. */
export function numberedBookSortForm(title: string): string | null {
  const m = NUMBERED_BOOK_RE.exec(title);
  if (!m) return null;
  const n = ordinalOf(m[1]);
  if (n === 0) return null;
  const rest = m[3].trim();
  return `${m[2]} ${n}${rest ? ` ${rest}` : ''}`;
}

/** The title sort key: numbered books rewritten, then trimmed + lower-cased. */
export function titleSortKey(title: string | null | undefined): string {
  const t = title ?? '';
  return (numberedBookSortForm(t) ?? t).trim().toLowerCase();
}
