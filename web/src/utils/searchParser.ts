// file: web/src/utils/searchParser.ts
// version: 1.7.1
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
   * Set when the query must be read by the server's own parser: it uses a
   * per-user field (read_status, progress_pct, last_played) AND a boolean form
   * the field/value split below cannot carry (an OR or AND, a parenthesised group, a
   * NOT over a group). The Library then sends this raw text as `search=` and no
   * field filters for it, so the server sees the OR and answers it, or refuses
   * it with a message. Without it `read_status:finished OR author:kyle` became
   * two filters that were ANDed, with the OR left as the free-text word "OR".
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

/**
 * Reports whether input uses a boolean form that field filters cannot carry:
 * an explicit operator (`OR`, `AND`, `||` or `&&`: the field/value split would leave the
 * word in the free text, where the server never reads it as an operator) or a
 * parenthesis, other than the `(a|b)` that is the
 * value of a field (`read_status:(finished|in_progress)`) and anything inside
 * double quotes.
 */
export function hasBooleanStructure(input: string): boolean {
  const bare = input.replace(/"[^"]*"?/g, ' ').replace(/\b\w+:\([^()]*\)/g, ' ');
  return /(^|\s)(OR|AND|\|\||&&)(\s|$)/.test(bare) || /[()]/.test(bare);
}

const PER_USER_FIELD_IN_TEXT = new RegExp(`\\b(?:${PER_USER_SEARCH_FIELDS.join('|')})\\s*:`, 'i');

/**
 * Reports whether input names a per-user field outside double quotes. Read from
 * the text, not from the parsed filters: inside a group the term is not a
 * filter at all (`(read_status:finished title:Hush)` parses to none), and the
 * server's refusal is exactly what the person needs to see there. Any letter
 * case counts, so `Read_Status:` reaches the server's "write read_status" message.
 */
export function mentionsPerUserField(input: string): boolean {
  return PER_USER_FIELD_IN_TEXT.test(input.replace(/"[^"]*"?/g, ' '));
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
  const fieldFilters: FieldFilter[] = [];
  const freeTextParts: string[] = [];

  if (!input.trim()) {
    return { freeText: '', fieldFilters: [] };
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
        freeTextParts.push(str.substring(pos));
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
              freeTextParts.push(word);
              freeTextParts.push(nextWord);
              pos = nextWordEnd === -1 ? str.length : nextWordEnd;
              continue;
            }
          }
        }
        freeTextParts.push(word);
        pos = wordEnd;
      }
    }
  }

  const parsed: ParsedSearch = {
    freeText: freeTextParts
      .filter((p) => p.length > 0)
      .join(' ')
      .trim(),
    fieldFilters,
  };
  if (mentionsPerUserField(input) && hasBooleanStructure(input)) {
    parsed.serverQuery = input.trim();
  }
  return parsed;
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
