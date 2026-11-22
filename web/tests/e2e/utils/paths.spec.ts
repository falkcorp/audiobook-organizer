// file: web/tests/e2e/utils/paths.spec.ts
// version: 0.1.0
// guid: e2e97a2a-7fe7-4997-84cd-91b2144fbc3d
// last-edited: 2026-11-22

// Checks on ./paths that do not open a browser. Every helper returns the value
// the specs spelled out before they were re-pointed to it; the literals below
// are copied from those specs, so they stay literal on purpose.

import { expect, test } from '@playwright/test';
import {
  bookPath,
  collectionPath,
  dedupPath,
  libraryPath,
  paths,
  reviewLanePath,
  reviewPath,
  settingsPath,
  urlContains,
  urlContainsAny,
  urlEndsWith,
  urlGlob,
  urlReaches,
} from './paths';

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
