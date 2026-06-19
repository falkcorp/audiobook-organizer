// file: web/src/utils/__tests__/formatPath.test.ts
// version: 1.0.0
// guid: c2f7a9e3-6b40-4d18-9a52-1e7c3d8b0f64
// last-edited: 2026-06-19

import { describe, it, expect } from 'vitest';
import { formatPath, derivePathVars } from '../formatPath';

const vars = [
  { name: 'libroot', value: '/mnt/bigdata/books/audiobook-organizer' },
  { name: 'books', value: '/mnt/bigdata/books' },
];

describe('formatPath', () => {
  it('abbreviates a path under libroot', () => {
    expect(formatPath('/mnt/bigdata/books/audiobook-organizer/Sanderson/Mistborn/01.m4b', vars)).toBe(
      '$(libroot)/Sanderson/Mistborn/01.m4b'
    );
  });

  it('abbreviates a path under books but not libroot', () => {
    expect(formatPath('/mnt/bigdata/books/itunes/iTunes Media/x.m4a', vars)).toBe(
      '$(books)/itunes/iTunes Media/x.m4a'
    );
  });

  it('prefers libroot over books (most-specific wins)', () => {
    expect(formatPath('/mnt/bigdata/books/audiobook-organizer/A/b.m4b', vars)).toBe('$(libroot)/A/b.m4b');
  });

  it('returns exact root as the bare token', () => {
    expect(formatPath('/mnt/bigdata/books/audiobook-organizer', vars)).toBe('$(libroot)');
    expect(formatPath('/mnt/bigdata/books', vars)).toBe('$(books)');
  });

  it('leaves unrelated paths unchanged', () => {
    expect(formatPath('/var/lib/audiobook-organizer/db.pebble', vars)).toBe(
      '/var/lib/audiobook-organizer/db.pebble'
    );
  });

  it('does not match a sibling that only shares a name prefix', () => {
    expect(formatPath('/mnt/bigdata/books-archive/x.m4b', vars)).toBe('/mnt/bigdata/books-archive/x.m4b');
  });

  it('skips empty-valued vars so they never match everything', () => {
    expect(formatPath('/some/path.m4b', [{ name: 'libroot', value: '' }])).toBe('/some/path.m4b');
  });
});

describe('derivePathVars', () => {
  it('derives libroot from rootDir and books from its parent', () => {
    expect(derivePathVars('/mnt/bigdata/books/audiobook-organizer')).toEqual(vars);
  });

  it('strips a trailing slash from rootDir', () => {
    expect(derivePathVars('/mnt/bigdata/books/audiobook-organizer/')).toEqual(vars);
  });

  it('returns empty for an empty rootDir', () => {
    expect(derivePathVars('')).toEqual([]);
  });
});
