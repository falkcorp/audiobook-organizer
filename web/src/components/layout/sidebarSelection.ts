// file: web/src/components/layout/sidebarSelection.ts
// version: 1.0.0
// guid: 9d41f0b7-2ac6-4e83-b5d0-61e7c9a3f882
// last-edited: 2026-08-08

/**
 * Decides whether a Library sub-item should render as selected.
 *
 * Lives in its own module so Sidebar.tsx only exports components
 * (react-refresh/only-export-components), and so this is unit-testable
 * without mounting the sidebar.
 *
 * Exported for tests. The previous implementation compared
 * `location.pathname` against `item.matchPath ?? item.path`, which is wrong in
 * both directions for the filter items: `pathname` never carries a query
 * string, so 'In Progress' (path '/library?search=...') could never match,
 * while 'All Books' (matchPath '/library') matched on *every* /library URL.
 * The highlight was therefore pinned to All Books permanently.
 *
 * Items that declare `matchSearch` are compared on the parsed, decoded
 * `search` param rather than the raw path, so they still match once
 * Library.tsx settles the URL into `?search=read_status%3Ain_progress&page=1`.
 */
export function isSubItemSelected(
  item: { path: string; matchPath?: string; matchSearch?: string },
  pathname: string,
  search: string,
): boolean {
  const itemPath = (item.matchPath ?? item.path).split('?')[0];
  if (pathname !== itemPath) return false;
  if (item.matchSearch === undefined) return true;
  return (new URLSearchParams(search).get('search') ?? '') === item.matchSearch;
}
