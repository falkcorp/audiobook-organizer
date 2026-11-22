// file: web/tests/e2e/utils/paths.ts
// version: 0.1.0
// guid: 2f91cdd9-a45d-4e62-b0e4-9fef67c93920
// last-edited: 2026-11-22

// The ONE place the e2e specs name an SPA page URL. `page.goto`, `toHaveURL`,
// `waitForURL` and the final-URL helpers take their paths from here, so moving
// the audiobook pages (F9-03 puts them under /books/, spec 07 section 7.11) is a
// change to this file and not to ~50 specs. paths.spec.ts pins every value
// against the literal the specs used before they were re-pointed, and fails
// when a spec spells a page path out again.
//
// API mock routes (`**/api/v1/**`) are NOT page URLs and stay in their specs.

/** Each page's path, query and hash left off. */
export const paths = {
  home: '/',
  dashboard: '/dashboard',
  login: '/login',
  library: '/library',
  settings: '/settings',
  diagnostics: '/diagnostics',
  collections: '/collections',
  series: '/series',
  authors: '/authors',
  operations: '/operations',
  activity: '/activity',
  review: '/review',
  dedup: '/dedup',
  dedupLabels: '/dedup/labels',
  // Old addresses that redirect into the Duplicates lane.
  authorsDedup: '/authors/dedup',
  booksDedup: '/books/dedup',
} as const;

/** One row of the old-to-new page-path table (07 section 7.11, last row). */
export interface PagePathMove {
  /** The path today (what `paths` holds, or a parameterised page). */
  from: string;
  /**
   * The path after F9-03's shell adoption, or null when no page lives there any
   * more: D13 redirects no former path, so a bookmark lands on the shell's NotFound.
   */
  to: string | null;
  /** Who serves it after the flip. */
  owner: 'shell' | 'books';
  note?: string;
}

/**
 * The second table: where each page goes at F9-03 (07 section 7.11). The e2e
 * specs still use `paths` above until the flip's commit 2 switches them to
 * `shellPaths`; paths.spec.ts pins every row so the two tables cannot drift from
 * the spec or from each other. Rows are in the order of the spec's table.
 */
export const PAGE_PATH_MOVES: readonly PagePathMove[] = [
  // Pages the shell owns.
  { from: '/', to: '/', owner: 'shell', note: 'today redirects to /dashboard; the shell home is the dashboard' },
  { from: '/dashboard', to: '/', owner: 'shell', note: '07 7.11: /dashboard -> /' },
  { from: '/login', to: '/login', owner: 'shell' },
  { from: '/setup', to: '/setup', owner: 'shell' },
  { from: '/invite/:token', to: '/invite/:token', owner: 'shell' },
  { from: '/users', to: '/users', owner: 'shell' },
  { from: '/settings', to: '/settings', owner: 'shell', note: 'the frame; books panels are settingsPanels' },
  { from: '/system', to: '/system', owner: 'shell' },
  { from: '/maintenance', to: '/system', owner: 'shell', note: '07 7.11: /maintenance -> /system (was a redirect to /system?tab=maintenance)' },
  { from: '/diagnostics', to: '/system/diagnostics', owner: 'shell', note: '07 7.11' },
  { from: '/activity', to: '/activity', owner: 'shell' },
  { from: '/operations', to: '/activity', owner: 'shell', note: '07 7.11: /operations -> /activity' },
  // Pages that move under /books/.
  { from: '/library', to: '/books/library', owner: 'books' },
  { from: '/library/:id', to: '/books/library/:id', owner: 'books' },
  { from: '/fingerprints', to: '/books/fingerprints', owner: 'books' },
  { from: '/works', to: '/books/works', owner: 'books' },
  { from: '/files', to: '/books/files', owner: 'books', note: '07 7.11: /files -> /books/files' },
  { from: '/series', to: '/books/series', owner: 'books' },
  { from: '/authors', to: '/books/authors', owner: 'books' },
  { from: '/authors/:id', to: '/books/authors/:id', owner: 'books' },
  { from: '/playlists', to: '/books/playlists', owner: 'books' },
  { from: '/playlists/:id', to: '/books/playlists/:id', owner: 'books' },
  { from: '/collections', to: '/books/collections', owner: 'books' },
  { from: '/collections/:id', to: '/books/collections/:id', owner: 'books' },
  { from: '/versions', to: '/books/versions', owner: 'books' },
  { from: '/review', to: '/books/review', owner: 'books' },
  // The four old addresses that redirect into a Review lane today (App.tsx
  // DedupRedirect). Each is a former path, and D13 allows no redirect from one:
  // commit 2 decides whether DedupRedirect goes. /books/dedup is listed too
  // because /books/ is books' prefix after the flip, so it must not stay a redirect.
  { from: '/dedup', to: null, owner: 'books', note: 'old-URL redirect today; D13 -> NotFound (confirm in commit 2)' },
  { from: '/dedup/labels', to: null, owner: 'books', note: 'as /dedup' },
  { from: '/authors/dedup', to: null, owner: 'books', note: 'as /dedup' },
  { from: '/books/dedup', to: null, owner: 'books', note: 'as /dedup; the /books/ prefix is books\' after the flip' },
];

/**
 * HEAD's ABS_REDIRECTS (web/src/utils/absRedirects.ts) with the targets 07 7.11
 * says they take after the flip. They map Audiobookshelf web-client URLs, not
 * ABO's own former paths, so D13 does not remove them. `/settings?tab=` is
 * unresolved: seams.d/F9-03-prep-pages.md lists the Settings tab strip versus
 * panel routes as commit 2's gap, so only the `/` target and the pathnames are settled.
 */
export const ABS_REDIRECT_TARGETS: ReadonlyArray<{ from: string; to: string }> = [
  { from: '/config', to: '/settings' },
  { from: '/config/api-keys', to: '/settings?tab=api-keys' },
  { from: '/audiobookshelf', to: '/' },
  { from: '/audiobookshelf/config', to: '/settings' },
  { from: '/audiobookshelf/config/api-keys', to: '/settings?tab=api-keys' },
];

/** The row for `from`; throws on a path the table does not list. */
function moveOf(from: string): PagePathMove {
  const row = PAGE_PATH_MOVES.find((m) => m.from === from);
  if (!row) throw new Error(`PAGE_PATH_MOVES has no row for ${from}`);
  return row;
}

/**
 * `paths` after the flip, by the same keys. null = no page there any more
 * (the shell's NotFound answers it).
 */
export const shellPaths = {
  home: moveOf(paths.home).to,
  dashboard: moveOf(paths.dashboard).to,
  login: moveOf(paths.login).to,
  library: moveOf(paths.library).to,
  settings: moveOf(paths.settings).to,
  diagnostics: moveOf(paths.diagnostics).to,
  collections: moveOf(paths.collections).to,
  series: moveOf(paths.series).to,
  authors: moveOf(paths.authors).to,
  operations: moveOf(paths.operations).to,
  activity: moveOf(paths.activity).to,
  review: moveOf(paths.review).to,
  dedup: moveOf(paths.dedup).to,
  dedupLabels: moveOf(paths.dedupLabels).to,
  authorsDedup: moveOf(paths.authorsDedup).to,
  booksDedup: moveOf(paths.booksDedup).to,
} as const satisfies Record<keyof typeof paths, string | null>;

/** `path` plus `?query` when there is one. `query` has no leading `?`. */
function withQuery(path: string, query?: string): string {
  return query ? `${path}?${query}` : path;
}

/** The library list, optionally with a query string (`sort=title&order=asc`). */
export function libraryPath(query?: string): string {
  return withQuery(paths.library, query);
}

/** One book's detail page, optionally with a query string (`tab=files`). */
export function bookPath(id: string, query?: string): string {
  return withQuery(`${paths.library}/${id}`, query);
}

/** Settings, optionally at a section (`paths` gives `/settings#paths`). */
export function settingsPath(section?: string): string {
  return section ? `${paths.settings}#${section}` : paths.settings;
}

/** The review page with a query string (`lane=dupes&layer=exact`). */
export function reviewPath(query: string): string {
  return withQuery(paths.review, query);
}

/** A review lane (`lane=authors`). */
export function reviewLanePath(lane: string): string {
  return reviewPath(`lane=${lane}`);
}

/** The legacy dedup page, optionally with a query string (`tab=authors`). */
export function dedupPath(query?: string): string {
  return withQuery(paths.dedup, query);
}

/** One collection's page. */
export function collectionPath(id: string): string {
  return `${paths.collections}/${id}`;
}

/** Escape `text` for use inside a RegExp source. */
function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** Matches a URL that ENDS with `path` (`/\/dashboard$/`). */
export function urlEndsWith(path: string): RegExp {
  return new RegExp(`${escapeRegExp(path)}$`);
}

/** Matches a URL that CONTAINS `path` anywhere (`/\/library\/b1/`). */
export function urlContains(path: string): RegExp {
  return new RegExp(escapeRegExp(path));
}

/** Matches any URL up to and including `path` (`/.*\/settings/`). */
export function urlReaches(path: string): RegExp {
  return new RegExp(`.*${escapeRegExp(path)}`);
}

/** Matches a URL that contains any one of `list` (`/\/(operations|activity)/`). */
export function urlContainsAny(list: readonly string[]): RegExp {
  return new RegExp(`/(${list.map((p) => escapeRegExp(p.replace(/^\//, ''))).join('|')})`);
}

/** A glob for `waitForURL`: any origin, then `path` (`**\/login`). */
export function urlGlob(path: string): string {
  return `**${path}`;
}
