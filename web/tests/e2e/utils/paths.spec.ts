// file: web/tests/e2e/utils/paths.spec.ts
// version: 0.1.0
// guid: e2e97a2a-7fe7-4997-84cd-91b2144fbc3d
// last-edited: 2026-11-22

// Two checks on ./paths, neither of which opens a browser.
//
// 1. Every helper returns the value the specs spelled out before they were
//    re-pointed to it (the literals below are copied from those specs, so they
//    stay literal on purpose: this file is exempt from the guard).
// 2. No spec spells out a page path in goto, toHaveURL, waitForURL or
//    expectFinalUrl, so F9-03's move of the pages under /books/ is a change to
//    paths.ts only. The guard's own samples show what it catches and allows.

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test } from '@playwright/test';
import { ABS_REDIRECTS } from '../../../src/utils/absRedirects';
import { findRawPagePaths } from './pathLiteralGuard';
import {
  ABS_REDIRECT_TARGETS,
  PAGE_PATH_MOVES,
  bookPath,
  collectionPath,
  dedupPath,
  libraryPath,
  paths,
  reviewLanePath,
  reviewPath,
  settingsPath,
  shellPaths,
  urlContains,
  urlContainsAny,
  urlEndsWith,
  urlGlob,
  urlReaches,
} from './paths';

const E2E_DIR = join(dirname(fileURLToPath(import.meta.url)), '..');

test.describe('paths: values the specs used before', () => {
  test('page paths', () => {
    expect(paths.home).toBe('/');
    expect(paths.dashboard).toBe('/dashboard');
    expect(paths.login).toBe('/login');
    expect(paths.library).toBe('/library');
    expect(paths.settings).toBe('/settings');
    expect(paths.diagnostics).toBe('/diagnostics');
    expect(paths.collections).toBe('/collections');
    expect(paths.series).toBe('/series');
    expect(paths.authors).toBe('/authors');
    expect(paths.operations).toBe('/operations');
    expect(paths.activity).toBe('/activity');
    expect(paths.review).toBe('/review');
    expect(paths.dedup).toBe('/dedup');
    expect(paths.dedupLabels).toBe('/dedup/labels');
    expect(paths.authorsDedup).toBe('/authors/dedup');
    expect(paths.booksDedup).toBe('/books/dedup');
  });

  test('library list and book detail', () => {
    expect(libraryPath()).toBe('/library');
    expect(libraryPath('sort=title&order=asc')).toBe('/library?sort=title&order=asc');
    expect(libraryPath('sort=author&order=desc')).toBe('/library?sort=author&order=desc');
    expect(libraryPath('sort=created_at&order=desc')).toBe('/library?sort=created_at&order=desc');
    expect(libraryPath('search=read_status:finished')).toBe('/library?search=read_status:finished');
    expect(libraryPath('tag=scifi')).toBe('/library?tag=scifi');
    expect(libraryPath('state=imported')).toBe('/library?state=imported');
    expect(libraryPath('limit=50000')).toBe('/library?limit=50000');
    expect(libraryPath(`limit=${1000}`)).toBe('/library?limit=1000');
    expect(libraryPath(`search=${encodeURIComponent('tag:"Robin"')}`)).toBe(
      `/library?search=${encodeURIComponent('tag:"Robin"')}`,
    );
    expect(bookPath('book-1')).toBe('/library/book-1');
    expect(bookPath('test-book-id')).toBe('/library/test-book-id');
    expect(bookPath('does-not-exist')).toBe('/library/does-not-exist');
    expect(bookPath('01ABC', 'tab=files')).toBe('/library/01ABC?tab=files');
  });

  test('settings, review, dedup, collections', () => {
    expect(settingsPath()).toBe('/settings');
    expect(settingsPath('paths')).toBe('/settings#paths');
    expect(settingsPath('security')).toBe('/settings#security');
    expect(reviewLanePath('dupes')).toBe('/review?lane=dupes');
    expect(reviewLanePath('authors')).toBe('/review?lane=authors');
    expect(reviewLanePath('metadata')).toBe('/review?lane=metadata');
    expect(reviewPath('lane=authors&view=ai')).toBe('/review?lane=authors&view=ai');
    expect(reviewPath('lane=authors&view=series')).toBe('/review?lane=authors&view=series');
    expect(reviewPath('lane=dupes&layer=exact')).toBe('/review?lane=dupes&layer=exact');
    expect(reviewPath('lane=dupes&view=labels')).toBe('/review?lane=dupes&view=labels');
    expect(reviewPath('lane=repairs&fixer=reconcile.missing-files')).toBe(
      '/review?lane=repairs&fixer=reconcile.missing-files',
    );
    expect(reviewPath('book=01ABC')).toBe('/review?book=01ABC');
    expect(dedupPath()).toBe('/dedup');
    expect(dedupPath('tab=authors')).toBe('/dedup?tab=authors');
    expect(dedupPath('tab=split-books')).toBe('/dedup?tab=split-books');
    expect(dedupPath('tab=unified&book=01ABCDEFGHIJKLMNOPQRSTUV01&foo=1')).toBe(
      '/dedup?tab=unified&book=01ABCDEFGHIJKLMNOPQRSTUV01&foo=1',
    );
    expect(collectionPath('c-static')).toBe('/collections/c-static');
  });

  // Same source and flags as the regex literals the specs passed before.
  test('URL patterns', () => {
    const same = (actual: RegExp, expected: RegExp) => {
      expect(actual.source).toBe(expected.source);
      expect(actual.flags).toBe(expected.flags);
    };
    same(urlEndsWith(paths.dashboard), /\/dashboard$/);
    same(urlEndsWith(paths.login), /\/login$/);
    same(urlEndsWith(paths.library), /\/library$/);
    same(urlEndsWith(collectionPath('c-static')), /\/collections\/c-static$/);
    same(urlEndsWith(collectionPath('c-dynamic')), /\/collections\/c-dynamic$/);
    same(urlReaches(paths.library), /.*\/library/);
    same(urlReaches(paths.settings), /.*\/settings/);
    same(urlContains(bookPath('b1')), /\/library\/b1/);
    same(urlContainsAny([paths.operations, paths.activity]), /\/(operations|activity)/);
    expect(urlGlob(paths.login)).toBe('**/login');
  });

  test('URL patterns still match and refuse what the literals did', () => {
    expect(urlEndsWith(paths.dashboard).test('http://x.test/dashboard')).toBe(true);
    expect(urlEndsWith(paths.dashboard).test('http://x.test/dashboard?a=1')).toBe(false);
    expect(urlContainsAny([paths.operations, paths.activity]).test('http://x.test/activity')).toBe(true);
    expect(urlContainsAny([paths.operations, paths.activity]).test('http://x.test/library')).toBe(false);
  });
});

test.describe('paths: no spec spells a page path out', () => {
  const sources = [
    ...readdirSync(E2E_DIR).filter((f) => f.endsWith('.spec.ts')),
    ...readdirSync(join(E2E_DIR, 'utils')).map((f) => join('utils', f)),
  ]
    .filter((f) => f.endsWith('.ts'))
    .filter((f) => !/(^|\/)(paths\.spec|paths|pathLiteralGuard)\.ts$/.test(f))
    .sort();

  test('the scan reads the specs', () => {
    expect(sources.length).toBeGreaterThan(40);
    expect(sources).toContain('library-browser.spec.ts');
    expect(sources).toContain(join('utils', 'setup-modes.ts'));
  });

  test('every spec takes its page paths from utils/paths', () => {
    const raw = sources.flatMap((file) =>
      findRawPagePaths(readFileSync(join(E2E_DIR, file), 'utf8')).map((hit) => `${file}:${hit.line}  ${hit.text}`),
    );
    expect(raw, 'raw page-path literals; use utils/paths').toEqual([]);
  });

  test('the guard catches each shape', () => {
    const bad = [
      "await page.goto('/library');",
      'await page.goto(`/library/${id}`);',
      "await page.goto(opts.url ?? '/review?lane=authors');",
      'await page.goto(\n  "/settings#paths",\n);',
      'await expect(page).toHaveURL(/\\/dashboard$/);',
      'await expect(page).toHaveURL(/.*\\/settings/);',
      "await page.waitForURL('**/login');",
      'await page.waitForURL(/.*\\/settings/, { timeout: 5000 });',
      "await expectFinalUrl(page, '/review?lane=dupes');",
      'page.locator(`a[href="/library/${id}"]`)',
      "expect(pageUrl).toContain('/settings');",
    ];
    for (const source of bad) {
      expect(findRawPagePaths(source), source).toHaveLength(1);
    }
  });

  test('the guard allows what names no page', () => {
    const fine = [
      'await page.goto(paths.library);',
      'await page.goto(libraryPath(`limit=${n}`));',
      "await page.goto(baseURL || 'http://localhost:5173');",
      'await page.goto(initialURL, { waitUntil: "domcontentloaded" });',
      'await expect(page).toHaveURL(/band=CERTAIN/);',
      'await expect(page).toHaveURL(/[?&]layer=acoustid(&|$)/);',
      'await expect(page).toHaveURL(expectedUrl, { timeout: 3000 });',
      "await page.route('**/api/v1/audiobooks**', handler);",
      "expect(url).toContain('sort=author');",
      "const key = url.pathname.split('/').pop();",
    ];
    for (const source of fine) {
      expect(findRawPagePaths(source), source).toEqual([]);
    }
  });
});

test.describe('paths: the shell table (07 section 7.11, F9-03)', () => {
  const moves = new Map(PAGE_PATH_MOVES.map((m) => [m.from, m]));

  test('every row names one old path once', () => {
    expect(moves.size).toBe(PAGE_PATH_MOVES.length);
    for (const m of PAGE_PATH_MOVES) {
      expect(m.from.startsWith('/')).toBe(true);
      if (m.to !== null) expect(m.to.startsWith('/')).toBe(true);
    }
  });

  test('every page path the specs use today has a row', () => {
    for (const [key, value] of Object.entries(paths)) {
      expect(moves.has(value), `paths.${key} (${value}) has no PAGE_PATH_MOVES row`).toBe(true);
    }
  });

  test('the spec rows, from the 07 section 7.11 table', () => {
    const to = (from: string) => moves.get(from)?.to;
    // Pages the shell absorbs.
    expect(to('/dashboard')).toBe('/');
    expect(to('/files')).toBe('/books/files');
    expect(to('/diagnostics')).toBe('/system/diagnostics');
    expect(to('/operations')).toBe('/activity');
    expect(to('/maintenance')).toBe('/system');
    // Every other audiobook page moves under /books/.
    expect(to('/library')).toBe('/books/library');
    expect(to('/library/:id')).toBe('/books/library/:id');
    expect(to('/series')).toBe('/books/series');
    expect(to('/authors')).toBe('/books/authors');
    expect(to('/collections')).toBe('/books/collections');
    expect(to('/review')).toBe('/books/review');
    // Shell pages keep their address.
    for (const keep of ['/login', '/setup', '/users', '/settings', '/system', '/activity']) {
      expect(to(keep), keep).toBe(keep);
    }
  });

  test('books pages are under /books/ and shell pages are not', () => {
    for (const m of PAGE_PATH_MOVES) {
      if (m.to === null) continue;
      expect(m.to.startsWith('/books/'), `${m.from} -> ${m.to} (${m.owner})`).toBe(m.owner === 'books');
    }
  });

  test('a former path that is not a new path is not reachable, so D13 holds', () => {
    // No old path may stay as a second route to a moved page: a "from" that
    // is moved must not also be some other row's "to".
    const targets = new Set(PAGE_PATH_MOVES.flatMap((m) => (m.to === null ? [] : [m.to])));
    for (const m of PAGE_PATH_MOVES) {
      if (m.to === null || m.to === m.from) continue;
      expect(targets.has(m.from), `${m.from} moved to ${m.to} but is also a destination`).toBe(false);
    }
  });

  test('shellPaths mirrors paths key for key', () => {
    expect(Object.keys(shellPaths).sort()).toEqual(Object.keys(paths).sort());
    expect(shellPaths.home).toBe('/');
    expect(shellPaths.dashboard).toBe('/');
    expect(shellPaths.library).toBe('/books/library');
    expect(shellPaths.diagnostics).toBe('/system/diagnostics');
    expect(shellPaths.operations).toBe('/activity');
    // The four old dedup addresses are former paths: NotFound after the flip.
    expect(shellPaths.dedup).toBeNull();
    expect(shellPaths.dedupLabels).toBeNull();
    expect(shellPaths.authorsDedup).toBeNull();
    expect(shellPaths.booksDedup).toBeNull();
  });

  test('ABS_REDIRECT_TARGETS keeps HEAD sources and points them at live pages', () => {
    expect(ABS_REDIRECT_TARGETS.map((r) => r.from)).toEqual(ABS_REDIRECTS.map((r) => r.from));
    const live = new Set(PAGE_PATH_MOVES.flatMap((m) => (m.to === null ? [] : [m.to])));
    for (const r of ABS_REDIRECT_TARGETS) {
      const pathname = r.to.split('?')[0];
      expect(live.has(pathname), `${r.from} -> ${r.to}`).toBe(true);
    }
  });
});
