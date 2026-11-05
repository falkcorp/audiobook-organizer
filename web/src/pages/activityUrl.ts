// file: web/src/pages/activityUrl.ts
// version: 1.0.0
// guid: 45954b0f-cd73-4c48-9acd-b356d25b83b2
// last-edited: 2026-11-05
//
// The Activity page's address-bar contract: which query parameter carries which
// piece of the feed's state, what the default of each is (a default is left out
// of the URL), and the two pure functions that go between the page's state and
// the query string. Kept out of ActivityLog.tsx so the mapping can be tested
// without mounting a 3,000-line page.
//
//   op         the operation whose log panel is open
//   q          text search
//   tiers      comma list of tiers, sorted; the four default tiers are omitted
//   type       event type          level      log level
//   operation  show only this operation's entries
//   since      start of the window; omitted for the default window, `all` for
//              no lower bound (an empty field is "all history", and an empty
//              value cannot be told apart from absent in a query string)
//   until      end of the window
//   hide_noop  `0` when no-op entries are shown (hiding them is the default)
//   tag        comma list of tags
//   page, size feed page (1) and page size (25)
//
// Not here on purpose: excluded sources (a stored preference, not a place),
// the per-section pages of the operations panel, and the mobile filter drawer.

import type { UrlValue } from '../hooks/useUrlState';

export const ACTIVITY_DEFAULT_TIERS: readonly string[] = ['audit', 'change', 'digest', 'info'];
export const ACTIVITY_DEFAULT_PAGE_SIZE = 25;

/** The page's filter state, as the URL sees it. */
export interface ActivityUrlState {
  op: string | null;
  search: string;
  tiers: ReadonlySet<string>;
  typeFilter: string;
  levelFilter: string;
  operationId: string;
  sinceFilter: string;
  untilFilter: string;
  hideNoOp: boolean;
  tagFilter: readonly string[];
  page: number;
  pageSize: number;
}

const csv = (value: string | null): string[] =>
  (value ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

const positiveInt = (value: string | null, fallback: number): number => {
  const n = Number(value);
  return Number.isInteger(n) && n > 0 ? n : fallback;
};

const sameSet = (a: ReadonlySet<string>, b: readonly string[]): boolean =>
  a.size === b.length && b.every((t) => a.has(t));

/**
 * The state a query string names. `defaultSince` is the page's own default
 * window start (it is "now minus 24h" and so differs on every mount).
 */
export function readActivityUrl(params: URLSearchParams, defaultSince: string): ActivityUrlState {
  const tiers = csv(params.get('tiers'));
  const since = params.get('since');
  return {
    op: params.get('op') || null,
    search: params.get('q') ?? '',
    tiers: new Set(tiers.length > 0 ? tiers : ACTIVITY_DEFAULT_TIERS),
    typeFilter: params.get('type') ?? '',
    levelFilter: params.get('level') ?? '',
    operationId: params.get('operation') ?? '',
    sinceFilter: since === null ? defaultSince : since === 'all' ? '' : since,
    untilFilter: params.get('until') ?? '',
    hideNoOp: params.get('hide_noop') !== '0',
    tagFilter: csv(params.get('tag')),
    page: positiveInt(params.get('page'), 1),
    pageSize: positiveInt(params.get('size'), ACTIVITY_DEFAULT_PAGE_SIZE),
  };
}

/** The owned query keys for a state; a default value is null (absent). */
export function activityUrlOwned(
  s: ActivityUrlState,
  defaultSince: string
): Record<string, UrlValue> {
  return {
    op: s.op,
    q: s.search,
    tiers: sameSet(s.tiers, ACTIVITY_DEFAULT_TIERS) ? null : [...s.tiers].sort().join(','),
    type: s.typeFilter,
    level: s.levelFilter,
    operation: s.operationId,
    since: s.sinceFilter === defaultSince ? null : s.sinceFilter === '' ? 'all' : s.sinceFilter,
    until: s.untilFilter,
    hide_noop: s.hideNoOp ? null : '0',
    tag: s.tagFilter.length > 0 ? [...s.tagFilter].join(',') : null,
    page: s.page > 1 ? String(s.page) : null,
    size: s.pageSize === ACTIVITY_DEFAULT_PAGE_SIZE ? null : String(s.pageSize),
  };
}

/** True when two states would send the feed different requests. */
export function activityFiltersDiffer(a: ActivityUrlState, b: ActivityUrlState): boolean {
  return (
    a.search !== b.search ||
    !sameSet(a.tiers, [...b.tiers]) ||
    a.typeFilter !== b.typeFilter ||
    a.levelFilter !== b.levelFilter ||
    a.operationId !== b.operationId ||
    a.sinceFilter !== b.sinceFilter ||
    a.untilFilter !== b.untilFilter ||
    a.hideNoOp !== b.hideNoOp ||
    a.tagFilter.join(',') !== b.tagFilter.join(',') ||
    a.pageSize !== b.pageSize
  );
}
