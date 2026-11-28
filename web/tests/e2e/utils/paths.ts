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
  // One-time-token link pages: the token rides in the fragment (F4-08).
  invite: '/invite',
  tempLogin: '/login/temp',
  // The retired query-string form of the temp-login link (deleted at the F4-08 flip).
  legacyTempLogin: '/auth/temp-login',
} as const;

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

/** An invite link as minted after the flip: the token in the fragment, never in the path or query. */
export function inviteLinkPath(token: string): string {
  return `${paths.invite}#token=${token}`;
}

/** The retired path-form invite link, `/invite/<token>`; it opens no invite. */
export function legacyInviteLinkPath(token: string): string {
  return `${paths.invite}/${token}`;
}

/** A temp-login link as minted after the flip: the token in the fragment. */
export function tempLoginLinkPath(token: string): string {
  return `${paths.tempLogin}#token=${token}`;
}

/** The retired query-string temp-login link, `/auth/temp-login?token=`; it signs nobody in. */
export function legacyTempLoginLinkPath(token: string): string {
  return `${paths.legacyTempLogin}?token=${token}`;
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
