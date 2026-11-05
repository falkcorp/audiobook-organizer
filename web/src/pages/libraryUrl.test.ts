// file: web/src/pages/libraryUrl.test.ts
// version: 1.0.0
// guid: 1c6d4f80-93a5-4e2b-b8d7-5a0e3f7c9b12
// last-edited: 2026-11-05

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, it, expect } from 'vitest';
import { LIBRARY_OWNED_PARAMS, keepForeignParams } from './libraryUrl';

describe('keepForeignParams', () => {
  it('copies unknown keys (including repeats) and skips every owned one', () => {
    const into = new URLSearchParams('page=2');
    keepForeignParams(
      new URLSearchParams('search=bat&utm=a&tag=noir&ref=x&ref=y&reset=1&page=9'),
      into
    );
    expect(into.toString()).toBe('page=2&utm=a&ref=x&ref=y');
  });
});

describe('LIBRARY_OWNED_PARAMS', () => {
  // The list has to cover every key the filter hook reads, or the page would
  // treat a real filter as someone else's and keep a stale copy of it.
  it('covers every key useLibraryFilters reads', () => {
    const src = readFileSync(resolve(__dirname, '../hooks/useLibraryFilters.ts'), 'utf8');
    const read = new Set([...src.matchAll(/searchParams\.get\('([a-z_]+)'\)/g)].map((m) => m[1]));
    expect(read.size).toBeGreaterThan(10);
    for (const key of read) expect(LIBRARY_OWNED_PARAMS.has(key), key).toBe(true);
  });
});
