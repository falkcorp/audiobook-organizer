// file: core/web/packages/shell/test/clientOperations.equivalence.test.ts
// version: 0.1.0
// guid: b5c8f8a7-9250-4584-8cad-34ecc7d38903
// last-edited: 2026-11-22

// What the shell's client must keep doing when its hand-typed operation table
// (api/interim.ts, now deleted) is replaced by the generated one: every route the shell calls
// is in core.json at the same method and path, with the same `public` flag, and
// a client reads that flag the same way. The expected rows are written out here,
// not imported, so the test outlives the table it was copied from.

import { coreOperations } from '@mediacore/api-client';
import { describe, expect, it } from 'vitest';
import { createShellClient } from '../src/api/client';

const API = '/api/core/v1';

// [method, path under /api/core/v1, public]: the routes the shell calls (07 §7.9.1).
const SHELL_ROUTES = [
  ['get', '/auth/status', true],
  ['get', '/me', false],
  ['post', '/auth/login', true],
  ['post', '/auth/logout', false],
  ['post', '/auth/setup', true],
  ['post', '/auth/bootstrap', true],
  ['get', '/auth/providers', true],
  ['post', '/auth/invites/lookup', true],
  ['post', '/auth/invites/accept', true],
  ['post', '/auth/temp-login/consume', true],
  ['get', '/settings/schema', false],
  ['get', '/settings', false],
  ['patch', '/settings', false],
] as const;

describe('the shell routes in the generated operation table', () => {
  it.each(SHELL_ROUTES)('%s %s is in core.json with public=%s', (method, path, isPublic) => {
    const found = coreOperations.filter((o) => o.method === method && o.path === `${API}${path}`);
    expect(found).toHaveLength(1);
    expect(found[0]?.public).toBe(isPublic);
  });

  it.each(SHELL_ROUTES)('a shell client treats %s %s as public=%s', (method, path, isPublic) => {
    const client = createShellClient({ buildId: 'test', shell: true, baseUrl: 'http://192.0.2.1' });
    expect(client.runtime.skipAuthRedirect(method, `${API}${path}`)).toBe(isPublic);
  });
});
