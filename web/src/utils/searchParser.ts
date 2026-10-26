// file: web/src/utils/searchParser.ts
// version: 1.7.4
// last-edited: 2026-10-26
// guid: ADC8CF65-5107-463A-891C-CABE8C1D74CF

/**
 * Advanced search parser supporting field:value syntax with negation and quoting.
 *
 * Examples:
 *   author:"Joshua Dalzelle" tag:scifi NOT narrator:heitsch space marine
 *   -tag:romance great books
 *   title:a*  -title:/^\s*\d/  year:[2015 TO 2020]
 *
 * Values are passed to the server VERBATIM (a /regex/ keeps its slashes) and
 * evaluated there by internal/querygrammar — see web/src/utils/queryGrammar.ts
 * for the grammar and the client-side pre-check.
 */

export interface FieldFilter {
  field: string;
  value: string;
  negated: boolean;
  quoted: boolean;
}

export interface ParsedSearch {
  freeText: string;
  fieldFilters: FieldFilter[];
  /**
   * Set when the query has boolean structure the field/value split below cannot
   * carry (see hasBooleanStructure): an OR, AND, `||`, `&&` or `|` next to a field
   * term, a field term inside a parenthesised group, an alternation, a `tag:` term
   * under NOT or `-`, a `tag:` term that is not one whole tag name. The Library then sends this raw text as `search=` and no
   * field filters for it, so the server's own parser reads it and answers it, or
   * refuses it with a message the Library shows. Without it the flat split dropped
   * `-tag:"to-read"`, turned `tag:noir OR tag:gotham` into a search for the word
   * "OR" ANDed with both tags, and sent `tag:noir*` as a tag named "noir*": a
   * list that looked narrowed and was not, with no message.
   */
  serverQuery?: string;
}

/**
 * The fields whose value is the signed-in listener's own state. Kept in step
 * with the Go side (internal/search PerUserFieldNames) by QueryHelp.test.tsx.
 */
export const PER_USER_SEARCH_FIELDS: readonly string[] = [
  'read_status',
  'progress_pct',
  'last_played',
] as const;

/**
 * The text a list request sends as `search=`: the raw query when the server must
 * read it (ParsedSearch.serverQuery), otherwise the free text left after the
 * field filters were taken out. One function for every request built from a
 * parsed search, so the list, its tag facets and a select-all-across-pages
 * cannot disagree.
 */
export function listSearchText(parsed: ParsedSearch | null, fallback: string): string {
  return parsed ? (parsed.serverQuery ?? parsed.freeText) : fallback;
}

/**
 * The field filters a list request sends as `filters=` for a parsed search: the
 * parsed terms except `tag:` (those travel as tags[], see libraryTagsParam), and
 * none at all when the server must read the query whole (serverQuery), because
 * its terms are already inside `search=` and would be applied twice.
 * `quoted` is carried so title:"a*" stays literal text while title:a* is a wildcard
 * (see internal/querygrammar).
 */
export function listFieldFilters(
  parsed: ParsedSearch | null
): Array<{ field: string; value: string; negated: boolean; quoted?: boolean }> {
  if (!parsed || parsed.serverQuery) return [];
  return parsed.fieldFilters
    .filter((ff) => ff.field !== 'tag')
    .map((ff) => ({
      field: ff.field,
      value: ff.value,
      negated: ff.negated,
      ...(ff.quoted ? { quoted: true } : {}),
    }));
}

/** A boost or fuzzy marker left on its own: `^2`, `~`, `~2`, `^1.5`. */
const TAG_MODIFIER_WORD = /^[\^~]\d*(\.\d+)?$/;

/** The closed alternation of a per-user field, `(finished|in_progress)`. */
const PER_USER_ALTERNATION_VALUE = /^\([^()]*\)$/;

/**
 * Whether one field term (already cut out of the input by tryMatchFieldValue, so its
 * value is exactly what the field filter would carry) is boolean structure the
 * field/value split cannot carry. The value of a field is never scanned for
 * parentheses or pipes: a `/regex/` (`title:/^(the|a) /`), a quoted value
 * (`title:"Year (One)"`) and a plain value (`title:Batman(1989)`) hold them as text,
 * and the filter layer reads them as such. What counts is only:
 *  - a `tag:` term under NOT or `-`: tags travel as tags[], which only narrows;
 *  - a `tag:` value that is not one whole name: an unquoted `*`, `~` or `^` anywhere in
 *    it, or one that opens with a comparison, a range or a group;
 *  - a non-tag unquoted value that opens a group, `title:(a|b)`, other than the
 *    closed alternation of a per-user field, which the split carries as one filter.
 */
function fieldTermIsStructure(
  field: string,
  value: string,
  quoted: boolean,
  negated: boolean
): boolean {
  if (field === 'tag') {
    if (negated) return true;
    if (quoted) return false;
    return /^[*<>=[(]/.test(value) || /[*~^]/.test(value);
  }
  if (quoted || !value.startsWith('(')) return false;
  return !(PER_USER_SEARCH_FIELDS.includes(field) && PER_USER_ALTERNATION_VALUE.test(value));
}

/**
 * A free word that is a binary operator the server's grammar reads: OR, AND, `||`,
 * `&&`, or a lone `|`. A bare NOT is not one: the tokenizer already takes `NOT field:x`
 * as a negated term, and a NOT that touches only free words changes nothing the split
 * does. A `|` inside or on the edge of a longer word is text in free text; it is
 * an alternation only inside a field value.
 */
function freeWordIsOperator(word: string): boolean {
  return word === 'OR' || word === 'AND' || word === '&&' || word === '||' || word === '|';
}

/**
 * The `(` a word opens and the `)` it closes, `-(` counting as a `(`. A `(` inside the
 * word (`Batman(1989)`) is text and pairs with the `)` after it, so it closes nothing.
 */
function wordGroupDelta(word: string): { opens: number; closes: number } {
  const opens = /^-?(\(+)/.exec(word)?.[1].length ?? 0;
  const inner = (word.match(/\(/g) ?? []).length - opens;
  const trailing = /(\)+)$/.exec(word)?.[1].length ?? 0;
  return { opens, closes: Math.max(0, trailing - inner) };
}

/**
 * Reports whether input has a form the field/value split cannot carry, so the
 * server must read it whole (ParsedSearch.serverQuery). It is decided from the same
 * tokens parseSearch cuts the input into, never from a second pass over the raw text
 * (a second grammar drifted: it counted the `(`, `)` and `|` inside a `/regex/` or
 * a plain value as groups, and sent `title:/^(the|a) /` to a search index that has no
 * regex). See fieldTermIsStructure for the terms that count on their own.
 *
 * Structure among free words alone does NOT count. `robin NOT joker`, `Batman (1987)`
 * and `a OR b` stay on the split: the free text goes to `search=` and the server's own
 * parser reads the operators in it, exactly as it always has. What the split cannot
 * carry is an operator or a group that binds a term it pulls out of the text, because
 * the field filters then drop out of `filters=` and are matched as whole words in the
 * index instead. So an OR, AND, `||`, `&&` or `|` counts only when it sits next to an
 * extracted field term (`title:x OR robin`), and an extracted term counts only while a
 * group opened by a free word is still open (`(robin title:x)`).
 *
 * A boost or fuzzy marker that follows a tag term, glued (`tag:"noir"^2`) or after a
 * space (`tag:noir ^2`), is the tag's: the server refuses it as one.
 *
 * A negated field filter on any other field (`-title:x`, `NOT narrator:y`) is NOT
 * structure: it is carried exactly as a filter, with the substring, glob and regex
 * matching of the filter layer. Sent whole it would change meaning: `-title:bat` would
 * stop excluding "Batman" (the index matches words), and the fields only the filter
 * layer knows (metadata:, ident:, codec:) would be refused in search=.
 */
export function hasBooleanStructure(input: string): boolean {
  return scanSearch(input).structure;
}

export const SEARCH_FIELDS: readonly string[] = [
  'title',
  'author',
  'narrator',
  'series',
  'series_number',
  'genre',
  'year',
  'language',
  'publisher',
  'edition',
  'description',
  'format',
  'duration',
  'has_duration',
  'metadata',
  // Identification state (server: internal/audiobooks/filter_ident.go):
  // unasked, asked_empty, has_candidates, applied, held, or missing
  // (unasked + asked_empty: no candidate fetched and nothing applied).
  'ident',
  'file_size',
  'bitrate',
  'codec',
  'sample_rate',
  'channels',
  'bit_depth',
  'quality',
  'library_state',
  'created_at',
  'updated_at',
  'isbn10',
  'isbn13',
  'asin',
  'work_id',
  'tag',
  'review',
  'has_cover',
  'has_written',
  'needs_writeback',
  'has_organized',
  'itunes_sync_status',
  'read_status',
  'progress_pct',
  'last_played',
  // Dates (server: internal/search/relative_date.go): date_added and
  // date_modified are indexed; `added` and `modified` are their aliases. All
  // take YYYY-MM-DD, now, or an offset such as -30d (see queryGrammar.ts).
  'date_added',
  'added',
  'date_modified',
  'modified',
] as const;

const searchFieldSet = new Set<string>(SEARCH_FIELDS);

function isKnownField(field: string): boolean {
  return searchFieldSet.has(field);
}

/**
 * Parse a search query string into structured free text and field filters.
 *
 * Supports:
 * - Plain text: `hello world`
 * - Field filters: `author:smith`
 * - Quoted values: `author:"Joshua Dalzelle"`
 * - Regex values: `title:/^\s*\p{L}/`, spaces allowed inside the slashes
 *   (`title:/chapter \d+/`); an unterminated `/…` runs to the end of the input
 *   so the server can report it rather than it silently vanishing
 * - NOT negation: `NOT narrator:heitsch`
 * - Dash negation: `-tag:romance`
 * - Mixed: `great books author:smith NOT tag:romance`
 */
export function parseSearch(input: string): ParsedSearch {
  const { freeText, fieldFilters, structure } = scanSearch(input);
  const parsed: ParsedSearch = { freeText, fieldFilters };
  if (structure) {
    parsed.serverQuery = input.trim();
  }
  return parsed;
}

/**
 * The one tokenizer: cuts input into field filters and free words, and decides from
 * those same tokens whether the input has boolean structure (hasBooleanStructure).
 */
function scanSearch(input: string): {
  freeText: string;
  fieldFilters: FieldFilter[];
  structure: boolean;
} {
  const fieldFilters: FieldFilter[] = [];
  const freeTextParts: string[] = [];
  let structure = false;
  // The previous token was a tag: term, so a modifier word right after it is the tag's.
  let afterTag = false;
  // Inside a double-quoted phrase that began in an earlier token
  // (`"Dark (Knight)"` is two words here): a phrase is text, never structure.
  let inQuote = false;
  // The kind of the previous token, and whether it was an operator word: an operator
  // is structure when an extracted field term sits on either side of it.
  let prev: 'field' | 'free' | null = null;
  let prevIsOperator = false;
  // Groups opened by free words and not yet closed: an extracted term inside one is
  // bound by the group, which the split cannot carry.
  let depth = 0;
  const freeWord = (word: string) => {
    freeTextParts.push(word);
    let bare = word;
    if (inQuote) {
      const close = word.indexOf('"');
      bare = close < 0 ? '' : word.slice(close + 1);
    }
    if ((word.match(/"/g) ?? []).length % 2 === 1) inQuote = !inQuote;
    bare = bare.replace(/"[^"]*"?/g, '');
    const operator = freeWordIsOperator(bare);
    if (operator && prev === 'field') structure = true;
    const { opens, closes } = wordGroupDelta(bare);
    depth = Math.max(0, depth + opens - closes);
    if (afterTag && TAG_MODIFIER_WORD.test(word)) structure = true;
    afterTag = false;
    prev = 'free';
    prevIsOperator = operator;
  };

  if (!input.trim()) {
    return { freeText: '', fieldFilters: [], structure: false };
  }

  let pos = 0;
  const str = input;

  while (pos < str.length) {
    // Skip whitespace
    if (str[pos] === ' ') {
      pos++;
      continue;
    }

    // Try to match NOT prefix
    let negated = false;
    const startPos = pos;

    if (str.substring(pos, pos + 4) === 'NOT ' && pos + 4 < str.length) {
      // Peek ahead to see if what follows is a valid field:value
      const afterNot = pos + 4;
      const fieldMatch = tryMatchFieldValue(str, afterNot);
      if (fieldMatch && isKnownField(fieldMatch.field)) {
        negated = true;
        pos = afterNot;
      }
    }

    // Try dash negation
    if (!negated && str[pos] === '-' && pos + 1 < str.length) {
      const fieldMatch = tryMatchFieldValue(str, pos + 1);
      if (fieldMatch && isKnownField(fieldMatch.field)) {
        negated = true;
        pos = pos + 1;
      }
    }

    // Try to match field:value at current position
    const fieldMatch = tryMatchFieldValue(str, pos);

    if (fieldMatch && isKnownField(fieldMatch.field)) {
      const trimmedValue = fieldMatch.value.trim();
      if (
        !inQuote &&
        (fieldTermIsStructure(fieldMatch.field, trimmedValue, fieldMatch.quoted, negated) ||
          prevIsOperator ||
          depth > 0)
      ) {
        structure = true;
      }
      afterTag = !inQuote && fieldMatch.field === 'tag';
      prev = 'field';
      prevIsOperator = false;
      if (!fieldMatch.quoted && !trimmedValue.startsWith('/')) {
        // A quote that opens inside a plain value (`title:x"a b"`) opens a phrase
        // that a later word closes, and one that closes a phrase (`"a title:b"`)
        // ends it: the quotes in the value count like those of a free word.
        if ((trimmedValue.match(/"/g) ?? []).length % 2 === 1) inQuote = !inQuote;
        depth = Math.max(0, depth - wordGroupDelta(trimmedValue).closes);
      }
      if (trimmedValue) {
        fieldFilters.push({
          field: fieldMatch.field,
          value: trimmedValue,
          negated,
          quoted: fieldMatch.quoted,
        });
      }
      pos = fieldMatch.endPos;
    } else {
      // Not a field:value — consume as free text word
      // Reset pos to startPos if we advanced past NOT/dash
      pos = startPos;
      const wordEnd = str.indexOf(' ', pos);
      if (wordEnd === -1) {
        // Check if this word is "NOT" and the rest after it is a non-field token
        freeWord(str.substring(pos));
        pos = str.length;
      } else {
        const word = str.substring(pos, wordEnd);
        // If this is "NOT" and next token is unknown field:value, treat both as free text
        if (word === 'NOT') {
          const nextWordEnd = str.indexOf(' ', wordEnd + 1);
          const nextWord =
            nextWordEnd === -1
              ? str.substring(wordEnd + 1)
              : str.substring(wordEnd + 1, nextWordEnd);
          const colonIdx = nextWord.indexOf(':');
          if (colonIdx > 0) {
            const field = nextWord.substring(0, colonIdx);
            if (!isKnownField(field)) {
              // NOT + unknown field — push both as free text
              freeWord(word);
              freeWord(nextWord);
              pos = nextWordEnd === -1 ? str.length : nextWordEnd;
              continue;
            }
          }
        }
        freeWord(word);
        pos = wordEnd;
      }
    }
  }

  return {
    freeText: freeTextParts
      .filter((p) => p.length > 0)
      .join(' ')
      .trim(),
    fieldFilters,
    structure,
  };
}

interface FieldValueMatch {
  field: string;
  value: string;
  quoted: boolean;
  endPos: number;
}

function tryMatchFieldValue(str: string, pos: number): FieldValueMatch | null {
  // Match field name (word chars and underscores)
  let fieldEnd = pos;
  while (fieldEnd < str.length && isWordChar(str[fieldEnd])) {
    fieldEnd++;
  }

  if (fieldEnd === pos || fieldEnd >= str.length || str[fieldEnd] !== ':') {
    return null;
  }

  const field = str.substring(pos, fieldEnd);
  const valueStart = fieldEnd + 1; // skip colon

  if (valueStart >= str.length) {
    return null;
  }

  // Check for quoted value
  if (str[valueStart] === '"') {
    const closeQuote = str.indexOf('"', valueStart + 1);
    if (closeQuote === -1) {
      // Unclosed quote — take rest of string as value
      return {
        field,
        value: str.substring(valueStart + 1),
        quoted: true,
        endPos: str.length,
      };
    }
    return {
      field,
      value: str.substring(valueStart + 1, closeQuote),
      quoted: true,
      endPos: closeQuote + 1,
    };
  }

  // Regex — `title:/chapter \d+/` may contain spaces, so it runs to the next
  // UNESCAPED slash, not the next space. The slashes stay in the value: the
  // server decides regex-vs-text from them, and handleRemoveFilter rebuilds
  // the token from the value. Anything glued after the closing slash (e.g. a
  // `/x/i` flag) stays in the value too, so the server rejects it visibly
  // instead of it leaking into free text. Unterminated: take the rest of the
  // input, like an unclosed quote.
  if (str[valueStart] === '/') {
    let close = -1;
    for (let i = valueStart + 1; i < str.length; i++) {
      if (str[i] === '\\') {
        i++;
        continue;
      }
      if (str[i] === '/') {
        close = i;
        break;
      }
    }
    if (close === -1) {
      return { field, value: str.substring(valueStart), quoted: false, endPos: str.length };
    }
    let end = close + 1;
    while (end < str.length && str[end] !== ' ') end++;
    return { field, value: str.substring(valueStart, end), quoted: false, endPos: end };
  }

  // A per-user alternation — `read_status:(finished | in_progress)` — may hold
  // spaces too, and runs to its closing parenthesis. Only these fields have an
  // alternation in a field filter; for any other field a `(` stays an ordinary
  // character, as before.
  if (str[valueStart] === '(' && PER_USER_SEARCH_FIELDS.includes(field)) {
    const close = str.indexOf(')', valueStart + 1);
    if (close !== -1) {
      return {
        field,
        value: str.substring(valueStart, close + 1),
        quoted: false,
        endPos: close + 1,
      };
    }
  }

  // Bracketed range — `duration:[10m TO 2h]` contains spaces, so it runs to
  // the closing bracket instead of the next space. Without this the value was
  // "[10m" and "TO 2h]" leaked into the free text.
  if (str[valueStart] === '[') {
    const close = str.indexOf(']', valueStart + 1);
    if (close !== -1) {
      return {
        field,
        value: str.substring(valueStart, close + 1),
        quoted: false,
        endPos: close + 1,
      };
    }
  }

  // Unquoted value — up to next space
  let valueEnd = valueStart;
  while (valueEnd < str.length && str[valueEnd] !== ' ') {
    valueEnd++;
  }

  if (valueEnd === valueStart) {
    return null; // empty value
  }

  return {
    field,
    value: str.substring(valueStart, valueEnd),
    quoted: false,
    endPos: valueEnd,
  };
}

function isWordChar(ch: string): boolean {
  return /\w/.test(ch);
}
