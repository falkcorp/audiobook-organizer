// file: web/src/services/invitesRoute.guard.test.ts
// version: 0.1.0
// guid: 26f1d66f-3bf7-4de7-8b6c-97bbc8bf93ef
// last-edited: 2026-11-22

// Guard (F4-08 B10sq): no file under web/src lists or revokes pending invites on core's NATIVE
// route. With authentication off core's list and delete answer 401 authentication_required
// (they need a signed-in user), and that code ends the session in the shared client. Books'
// routes keep HEAD's reach (users.manage by any method, or auth off), so the Users page lists and
// revokes through them (services/booksInvites.ts). Core's invite CREATE is a different route and
// is not matched here.
//
// The allow-list is EMPTY and must stay empty: this file scans itself too, so its own patterns are
// assembled from pieces and never spell a path.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const SRC = join(__dirname, '..');
const INVITES = '/users' + '/invites';
const CORE = '/api/core' + '/v1';
const Q = '[\'"`]';

/** Files that may name a core-native invite list or delete. Empty. */
const ALLOWED: readonly string[] = [];

// 1. A client call: .GET( or .DELETE( whose first argument is a path ending in the invite segment
//    (the generated client's path key, with or without the core prefix), across line breaks.
// 2. The core prefix and the invite segment in one literal, anywhere but as the argument of .POST(
//    (create).
// 3. A test stub key for the same two calls (the method, a space, then the path without the prefix).
const NATIVE = [
  new RegExp('\\.(GET|DELETE)\\(\\s*' + Q + '[^\'"`]*' + INVITES),
  new RegExp('(?<!\\.POST\\(\\s*' + Q + ')' + CORE + INVITES),
  new RegExp(Q + '(GET|DELETE) ' + INVITES),
];

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) return name === 'node_modules' ? [] : walk(p);
    return /\.(ts|tsx|js|jsx|mjs)$/.test(name) ? [p] : [];
  });
}

function nativeInviteCalls(files: string[]): string[] {
  const hits: string[] = [];
  for (const file of files) {
    const rel = relative(SRC, file);
    if (ALLOWED.includes(rel)) continue;
    const text = readFileSync(file, 'utf8');
    for (const re of NATIVE) {
      const g = new RegExp(re.source, 'g');
      for (const m of text.matchAll(g)) {
        const line = text.slice(0, m.index).split('\n').length;
        hits.push(`${rel}:${line}: ${m[0].trim()}`);
      }
    }
  }
  return hits;
}

describe('invite list and revoke never use core\'s native routes', () => {
  it('the allow-list is empty', () => {
    expect(ALLOWED).toEqual([]);
  });

  it('no file under web/src calls a core-native invite list or delete', () => {
    expect(nativeInviteCalls(walk(SRC))).toEqual([]);
  });

  it('the guard itself matches the shapes it exists to catch, and not create or books', () => {
    const probe = (s: string) => NATIVE.some((re) => re.test(s));
    expect(probe("core().GET('" + CORE + INVITES + "', { params })")).toBe(true);
    expect(probe("core().DELETE('" + CORE + INVITES + "/{id}', { params })")).toBe(true);
    expect(probe("core().GET(\n    '" + CORE + INVITES + "'")).toBe(true);
    expect(probe("core().GET('" + INVITES + "')")).toBe(true);
    expect(probe("fetch(`" + CORE + INVITES + "`)")).toBe(true);
    expect(probe("'GET " + INVITES + "': () => page([])")).toBe(true);
    expect(probe("'DELETE " + INVITES + "/inv-1': () => new Response(null)")).toBe(true);
    // Create is core's and stays; books' list and delete are the ones to use.
    expect(probe("core().POST('" + CORE + INVITES + "', { body })")).toBe(false);
    expect(probe("apiFetch('/api/v1" + INVITES + "')")).toBe(false);
    expect(probe("'GET /api/v1" + INVITES + "': () => envelope({})")).toBe(false);
  });
});
