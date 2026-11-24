// file: web/src/services/apiKeysRoute.guard.test.ts
// version: 0.1.0
// guid: d0ca795f-7c23-4324-8e7d-1c8e13daeb36
// last-edited: 2026-11-22

// Guard (F4-08 B10sk): no file under web/src calls an API-key route of core's native
// API. A key minted there gets none of the books ambient scopes (core:self,
// core:metrics.read, books:reading), so one made in Settings and handed to an
// audiobook client could not sync progress. The books route does the minting.
//
// The allow-list is EMPTY and must stay empty: this file scans itself too, so its own
// patterns are assembled from pieces and never spell a path.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const SRC = join(__dirname, '..');
const KEYS = 'api' + '-keys';
const CORE = '/api/core' + '/v1';

/** Files that may name a core-native key path. Empty. */
const ALLOWED: readonly string[] = [];

// A core-native key path: the core prefix followed, in the same literal, by the key
// segment; or core's two key-route shapes by themselves (the caller's own, and a
// user's), which a base-URL constant would hide from the first pattern.
const NATIVE = [
  new RegExp(CORE + '[^\\s\'"`]*' + KEYS),
  new RegExp('/me/' + KEYS),
  new RegExp('/users/(\\{[a-z]+\\}|\\$\\{[^}]+\\}|[^/\\s\'"`]+)/' + KEYS),
];

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) return name === 'node_modules' ? [] : walk(p);
    return /\.(ts|tsx|js|jsx|mjs)$/.test(name) ? [p] : [];
  });
}

function nativeKeyCalls(files: string[]): string[] {
  const hits: string[] = [];
  for (const file of files) {
    const rel = relative(SRC, file);
    if (ALLOWED.includes(rel)) continue;
    readFileSync(file, 'utf8')
      .split('\n')
      .forEach((line, i) => {
        if (NATIVE.some((re) => re.test(line))) hits.push(`${rel}:${i + 1}: ${line.trim()}`);
      });
  }
  return hits;
}

describe('API keys never use core\'s native key routes', () => {
  it('the allow-list is empty', () => {
    expect(ALLOWED).toEqual([]);
  });

  it('no file under web/src calls a core-native API-key path', () => {
    expect(nativeKeyCalls(walk(SRC))).toEqual([]);
  });

  it('the guard itself matches the shapes it exists to catch', () => {
    const probe = (s: string) => NATIVE.some((re) => re.test(s));
    expect(probe("core().POST('" + CORE + "/me/" + KEYS + "', { body })")).toBe(true);
    expect(probe("core().POST('" + CORE + "/users/{id}/" + KEYS + "', {})")).toBe(true);
    expect(probe("core().GET('" + CORE + '/' + KEYS + "')")).toBe(true);
    expect(probe("`${BASE}/me/" + KEYS + "`")).toBe(true);
    // The books route is the one a key call may use.
    expect(probe('/api/v1/auth/' + KEYS)).toBe(false);
    expect(probe('/api/v1/auth/' + KEYS + '/k1')).toBe(false);
  });
});
