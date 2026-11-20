// file: core/web/packages/shell/test/rbac.vectors.test.ts
// version: 0.1.0
// guid: 4e8b1d62-9c05-4a37-b1f6-7d3a5c0e8f29
// last-edited: 2026-11-20

// 05-rbac §4.10 and the "UI hints" row of §4.10's table: the shell's covers()
// is run against the matching vectors Go's TestChecker_MatchVectors runs
// (core/rbac/testdata/match_vectors.json), so the two cannot disagree about
// what a pattern means.
//
// The client holds only what GET /me returned (concrete, already expanded
// permissions), and the server is authoritative: implication closure, key
// narrowing, Sensitive and AuthRequired scopes, confinement and the admin
// bypass are all decided server-side. A vector is run here only when its
// answer is decided by pattern matching alone, which is what covers() is for;
// serverOnly() names the reason for each vector it skips, and a new vector that
// covers() can decide is run without anyone editing this file.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { covers } from '../src/auth/hooks';

interface CatalogEntry {
  name: string;
  sensitive: boolean;
  auth_required: boolean;
  confining: boolean;
  implies: string[];
}

interface Vector {
  name: string;
  catalog: CatalogEntry[];
  roles: string[];
  key_scopes: string[] | null;
  interactive: boolean;
  check: { op: string; scope?: string };
  expected: boolean;
}

// core/web/packages/shell/test is four levels below core/ (and, in the split
// repository, web/packages/shell/test is four levels below the root): either
// way the vectors are at rbac/testdata.
const raw = readFileSync(resolve(__dirname, '../../../../rbac/testdata/match_vectors.json'), 'utf8');
const vectors = JSON.parse(raw) as Vector[];

const ns = (scope: string): string => scope.slice(0, Math.max(scope.indexOf(':'), 0));
const isPattern = (scope: string): boolean => scope.endsWith('*');

/** Why covers() cannot decide `v`, or null when pattern matching alone does. */
function serverOnly(v: Vector): string | null {
  if (v.check.op !== 'has' || v.check.scope === undefined) return `op ${v.check.op} is a requirement or confinement check`;
  if (v.key_scopes !== null) return 'a key narrows the roles';
  if (v.catalog.some((c) => c.implies.length > 0)) return 'implication closure';
  const scope = v.check.scope;
  const spaces = new Set([...v.roles.map(ns), ns(scope)]);
  if (spaces.has('core')) return 'the admin bypass (core:admin, core:*)';
  if (v.catalog.some((c) => spaces.has(ns(c.name)) && (c.sensitive || c.auth_required))) {
    return 'a Sensitive or AuthRequired scope in the namespace';
  }
  const entry = v.catalog.find((c) => c.name === scope);
  if (!entry && !isPattern(scope) && /^[a-z0-9_.-]+:[a-z0-9_.-]+$/.test(scope)) return 'a scope the catalog lacks';
  return null;
}

describe('rbac match vectors', () => {
  it('reads the whole vector file', () => {
    expect(vectors.length).toBeGreaterThanOrEqual(40);
    expect(new Set(vectors.map((v) => v.name)).size).toBe(vectors.length);
  });

  const decided = vectors.filter((v) => serverOnly(v) === null);

  it('runs a meaningful share of the vectors, both outcomes', () => {
    expect(decided.length).toBeGreaterThanOrEqual(10);
    expect(decided.some((v) => v.expected)).toBe(true);
    expect(decided.some((v) => !v.expected)).toBe(true);
  });

  it.each(decided.map((v) => [v.name, v] as const))('%s', (_name, v) => {
    const got = v.roles.some((held) => covers(held, v.check.scope as string));
    expect(got).toBe(v.expected);
  });
});
