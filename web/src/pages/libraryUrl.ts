// file: web/src/pages/libraryUrl.ts
// version: 1.0.0
// guid: 7e3b9a52-4d18-4c6f-a0b7-2f91c5d8e346
// last-edited: 2026-11-05

/**
 * The query keys the Library page owns: the ones it reads from the address bar and
 * writes back from state. The page rebuilds its query string on every state
 * change, so this list is what separates "mine, rebuild it" from "somebody else's,
 * leave it alone" (a link that carries a parameter this page does not know keeps
 * it across a page change or a filter edit).
 *
 * `reset` is here because it is a one-shot instruction, not state: the page
 * consumes it and the rebuilt URL must not carry it forward.
 *
 * Keep this in step with the keys `useLibraryFilters` reads and the writer in
 * Library.tsx sets; `libraryUrl.test.ts` fails if a filter key is missing.
 */
export const LIBRARY_OWNED_PARAMS: ReadonlySet<string> = new Set([
  'search',
  'author',
  'series',
  'genre',
  'language',
  'state',
  'version_group_id',
  'is_primary_version',
  'series_id',
  'sort',
  'order',
  'view',
  'page',
  'limit',
  'tag',
  'has_file_errors',
  'fingerprint_status',
  'coverage_percent_min',
  'coverage_percent_max',
  'missing_covers',
  'in_import_path',
  'no_isbn',
  'duplicates_flagged',
  'reset',
]);

/** Copies the entries of `current` that the Library page does not own into `into`. */
export function keepForeignParams(current: URLSearchParams, into: URLSearchParams): void {
  for (const [key, value] of current) {
    if (!LIBRARY_OWNED_PARAMS.has(key)) into.append(key, value);
  }
}
