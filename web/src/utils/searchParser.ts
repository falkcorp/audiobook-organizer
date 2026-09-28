// file: web/src/utils/searchParser.ts
// version: 1.3.0
// last-edited: 2026-09-27
// guid: ADC8CF65-5107-463A-891C-CABE8C1D74CF

/**
 * Advanced search parser supporting field:value syntax with negation and quoting.
 *
 * Examples:
 *   author:"Joshua Dalzelle" tag:scifi NOT narrator:heitsch space marine
 *   -tag:romance great books
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

    if (
      str.substring(pos, pos + 4) === 'NOT ' &&
      pos + 4 < str.length
    ) {
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

  return {
    freeText: freeTextParts
      .filter((p) => p.length > 0)
      .join(' ')
      .trim(),
    fieldFilters,
  };
}

interface FieldValueMatch {
  field: string;
  value: string;
  quoted: boolean;
  endPos: number;
}

function tryMatchFieldValue(
  str: string,
  pos: number
): FieldValueMatch | null {
  // Match field name (word chars and underscores)
  let fieldEnd = pos;
  while (
    fieldEnd < str.length &&
    (isWordChar(str[fieldEnd]))
  ) {
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
