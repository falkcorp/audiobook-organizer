// file: web/src/components/layout/sidebarSelection.test.ts
// version: 1.0.0
// guid: 5c1d9e42-7f38-4a61-9b0e-2d84c6f1a705
// last-edited: 2026-08-08

import { describe, expect, it } from 'vitest';

import { isSubItemSelected } from './sidebarSelection';

// Mirrors the librarySubItems entries under test. Kept local so the test
// documents the contract rather than depending on the array's ordering.
const ALL_BOOKS = { path: '/library?reset=1', matchPath: '/library', matchSearch: '' };
const IN_PROGRESS = {
  path: '/library?search=read_status:in_progress',
  matchSearch: 'read_status:in_progress',
};
const FINISHED = {
  path: '/library?search=read_status:finished',
  matchSearch: 'read_status:finished',
};
const AUTHORS = { path: '/authors' };

describe('isSubItemSelected', () => {
  it('selects All Books on a plain /library visit', () => {
    expect(isSubItemSelected(ALL_BOOKS, '/library', '')).toBe(true);
  });

  it('selects All Books when only pagination is in the URL', () => {
    expect(isSubItemSelected(ALL_BOOKS, '/library', '?page=1')).toBe(true);
  });

  // The original bug: All Books declared matchPath '/library' and was compared
  // against location.pathname, so it matched EVERY /library URL and the
  // highlight was pinned there no matter which filter was active.
  it('does NOT select All Books while a read_status filter is active', () => {
    expect(
      isSubItemSelected(ALL_BOOKS, '/library', '?search=read_status%3Ain_progress&page=1'),
    ).toBe(false);
  });

  // The other half of the original bug: In Progress had no matchPath, so
  // pathname ('/library') was compared against a path carrying a query string
  // and could never match.
  it('selects In Progress on the URL the sidebar navigates to', () => {
    expect(
      isSubItemSelected(IN_PROGRESS, '/library', '?search=read_status:in_progress'),
    ).toBe(true);
  });

  // The trap: once Library.tsx settles the URL it percent-encodes the colon and
  // appends page=1, so any raw string comparison against item.path fails.
  it('still selects In Progress once the URL is percent-encoded and paginated', () => {
    expect(
      isSubItemSelected(IN_PROGRESS, '/library', '?search=read_status%3Ain_progress&page=1'),
    ).toBe(true);
  });

  it('does not select In Progress on a plain /library visit', () => {
    expect(isSubItemSelected(IN_PROGRESS, '/library', '')).toBe(false);
  });

  it('does not select In Progress when a different filter is active', () => {
    expect(
      isSubItemSelected(IN_PROGRESS, '/library', '?search=read_status%3Afinished'),
    ).toBe(false);
  });

  it('selects Finished only for its own filter', () => {
    expect(
      isSubItemSelected(FINISHED, '/library', '?search=read_status%3Afinished&page=1'),
    ).toBe(true);
    expect(
      isSubItemSelected(FINISHED, '/library', '?search=read_status%3Ain_progress'),
    ).toBe(false);
  });

  it('never selects a library item on a different route', () => {
    expect(isSubItemSelected(ALL_BOOKS, '/authors', '')).toBe(false);
    expect(isSubItemSelected(IN_PROGRESS, '/series', '')).toBe(false);
  });

  it('matches query-free items on pathname alone, ignoring the query string', () => {
    expect(isSubItemSelected(AUTHORS, '/authors', '')).toBe(true);
    expect(isSubItemSelected(AUTHORS, '/authors', '?page=3')).toBe(true);
    expect(isSubItemSelected(AUTHORS, '/library', '')).toBe(false);
  });

  it('treats exactly one item as selected for any given library URL', () => {
    const items = [ALL_BOOKS, IN_PROGRESS, FINISHED];
    for (const search of ['', '?page=1', '?search=read_status%3Ain_progress&page=1', '?search=read_status%3Afinished']) {
      const selected = items.filter((i) => isSubItemSelected(i, '/library', search));
      expect(selected).toHaveLength(1);
    }
  });
});
