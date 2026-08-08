// file: web/src/components/library/libraryContentState.ts
// version: 1.0.0
// guid: 7e3a5c81-46bd-4f02-9c18-3d5b9e07a2f4
// last-edited: 2026-08-08

/** Which of the three mutually-exclusive Library bodies to render. */
export type LibraryContentState = 'reconnecting' | 'empty' | 'content';

/**
 * Decides what the Library shows when it has no books to display.
 *
 * Lives in its own module rather than in LibraryBookGrid.tsx so that file only
 * exports components (react-refresh/only-export-components), and so this can
 * be unit-tested without mounting the grid and its ~50 props.
 *
 * The ordering IS the fix. The previous condition was
 * `audiobooks.length === 0 && !loading && !searchQuery`, with no error branch
 * anywhere — so a failed request, which also leaves the list empty and
 * `loading` false, rendered "No Audiobooks Found". The backend is unreachable
 * for ~40s after every deploy while memdb warms over the whole library, so a
 * routine restart told the user their 44,000-book library was empty.
 *
 * `reconnecting` is therefore checked BEFORE `empty`: only a load that
 * RESOLVED with zero results may claim the library is empty.
 */
export function libraryContentState({
  bookCount,
  loading,
  loadError,
  searchQuery,
}: {
  bookCount: number;
  loading: boolean;
  loadError?: Error | null;
  searchQuery: string;
}): LibraryContentState {
  // Anything to show, or a request still in flight — the normal body handles
  // both, including its own in-grid spinner.
  if (bookCount > 0 || loading) return 'content';
  // Settled, but it FAILED. Never claim emptiness on the strength of a request
  // that did not come back.
  if (loadError) return 'reconnecting';
  // Settled successfully with zero results. A search that matched nothing is
  // handled inside the normal body, which offers to clear the query.
  if (!searchQuery) return 'empty';
  return 'content';
}
