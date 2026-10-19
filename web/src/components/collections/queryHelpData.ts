// file: web/src/components/collections/queryHelpData.ts
// version: 1.0.0
// guid: 7793a6b3-1c06-4470-ae92-563d9c29b9c4
// last-edited: 2026-10-18

import { firstSearchError } from '../../utils/queryGrammar';
import { parseSearch } from '../../utils/searchParser';

export type QueryFieldGroup = 'book' | 'date' | 'per_user';

export interface QueryFieldHelp {
  name: string;
  example: string;
  description: string;
  group: QueryFieldGroup;
}

/**
 * The fields the query box documents. ONE constant so the test can assert it,
 * and so a renamed index field fails QueryHelp.test.tsx (which reads the Go
 * sources) instead of the help quietly describing a field that no longer exists.
 *
 * rating, itunes_last_played and itunes_play_count are NOT here on purpose:
 * they are sort fields only (internal/playlist/evaluator.go) and a query on one
 * would match nothing. They live in SORT_FIELDS.
 */
export const QUERY_FIELD_HELP: readonly QueryFieldHelp[] = [
  { name: 'title', example: 'title:test*', description: 'word, "phrase", /RE2/ or glob', group: 'book' },
  { name: 'author', example: 'author:smith', description: 'author name', group: 'book' },
  { name: 'narrator', example: 'narrator:jones', description: 'narrator name', group: 'book' },
  { name: 'series', example: 'series:"test series"', description: 'series name', group: 'book' },
  { name: 'format', example: 'format:m4b', description: 'file format', group: 'book' },
  { name: 'genre', example: 'genre:fantasy', description: 'genre', group: 'book' },
  { name: 'year', example: 'year:>=2015', description: 'publication year; numeric ranges', group: 'book' },
  {
    name: 'duration',
    example: 'duration:>20h',
    description: 'human units (20m, 1h30m); a bare number is seconds. Ranges work: duration:30m..2h',
    group: 'book',
  },
  {
    name: 'date_added',
    example: 'date_added:>-1y',
    description: 'when the book was added; alias: added',
    group: 'date',
  },
  {
    name: 'date_modified',
    example: 'date_modified:>-30d',
    description: 'when the book was last changed; alias: modified',
    group: 'date',
  },
  {
    name: 'read_status',
    example: 'read_status:unstarted',
    description: 'unstarted, in_progress, finished or abandoned',
    group: 'per_user',
  },
  {
    name: 'progress_pct',
    example: 'progress_pct:>50',
    description: 'percent listened, 0 to 100',
    group: 'per_user',
  },
  {
    name: 'last_played',
    example: 'last_played:>-30d',
    description: 'relative (-30d; units d, w, m, y) or absolute (YYYY-MM-DD) dates',
    group: 'per_user',
  },
];

/** Query fields answered from one user's reading state, not from the book. */
export const PER_USER_FIELDS: readonly string[] = QUERY_FIELD_HELP.filter(
  (f) => f.group === 'per_user'
).map((f) => f.name);

export const PER_USER_NOTE =
  'These fields use the reading state of the person who created this collection, for everyone, including the Audiobookshelf app.';

export interface WorkedExample {
  title: string;
  query: string;
  note?: string;
}

/** Both strings are exactly what the backend (07-F5a) tests. */
export const WORKED_EXAMPLES: readonly WorkedExample[] = [
  {
    title: 'Finished in the last 30 days',
    query: 'read_status:finished last_played:>-30d',
    note: 'Relative dates roll forward each time the collection refreshes.',
  },
  { title: 'Over 20 hours, not started', query: 'duration:>20h read_status:unstarted' },
];

export interface SortField {
  value: string;
  label: string;
  /** True when the field can order results but cannot be used in a query. */
  sortOnly: boolean;
}

export const SORT_FIELDS: readonly SortField[] = [
  { value: 'title', label: 'Title', sortOnly: false },
  { value: 'year', label: 'Year', sortOnly: false },
  { value: 'audiobook_year', label: 'Audiobook year', sortOnly: false },
  { value: 'duration', label: 'Duration', sortOnly: false },
  { value: 'rating', label: 'Rating (sort only)', sortOnly: true },
  { value: 'date_added', label: 'Date added', sortOnly: false },
  { value: 'date_modified', label: 'Date modified', sortOnly: false },
  { value: 'itunes_last_played', label: 'iTunes last played (sort only)', sortOnly: true },
  { value: 'itunes_play_count', label: 'iTunes play count (sort only)', sortOnly: true },
];

export type SortDirection = 'asc' | 'desc';

export interface SortChoice {
  /** '' means no sort directive. */
  field: string;
  direction: SortDirection;
}

/** The `sort_json` string for a choice; '' when none (the server then orders by id). */
export function encodeSort(choice: SortChoice): string {
  return choice.field ? JSON.stringify([{ field: choice.field, direction: choice.direction }]) : '';
}

/** First directive of a stored `sort_json`; anything unreadable is "no sort". */
export function decodeSort(sortJson: string | undefined): SortChoice {
  if (!sortJson) return { field: '', direction: 'asc' };
  try {
    const parsed = JSON.parse(sortJson) as Array<{ field?: string; direction?: string }>;
    const first = Array.isArray(parsed) ? parsed[0] : undefined;
    if (first?.field) {
      return { field: first.field, direction: first.direction === 'desc' ? 'desc' : 'asc' };
    }
  } catch {
    // fall through
  }
  return { field: '', direction: 'asc' };
}

/** True when the query text filters on a per-user field (negated or not). */
export function usesPerUserField(query: string): boolean {
  return parseSearch(query).fieldFilters.some((f) => PER_USER_FIELDS.includes(f.field));
}

/** The first certain parse error in the query, or null. The server stays the authority. */
export function queryError(query: string): string | null {
  return firstSearchError(parseSearch(query).fieldFilters);
}

/** Field names that complete the token being typed (the text after the last space). */
export function suggestFields(query: string): string[] {
  const token = query.slice(query.search(/\S*$/));
  const bare = token.replace(/^(-|NOT)/, '');
  if (!bare || bare.includes(':')) return [];
  return QUERY_FIELD_HELP.map((f) => f.name).filter((n) => n.startsWith(bare) && n !== bare);
}

export function applySuggestion(query: string, field: string): string {
  const start = query.search(/\S*$/);
  const token = query.slice(start);
  const prefix = token.startsWith('-') ? '-' : '';
  return `${query.slice(0, start)}${prefix}${field}:`;
}
