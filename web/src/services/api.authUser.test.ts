// file: web/src/services/api.authUser.test.ts
// version: 1.0.0
// guid: 4c8e1f27-9a35-4d60-b7e2-0f3a5d9c6b18
// last-edited: 2026-10-18
//
// AuthUser carries the caller's effective permissions so the UI can hide write
// controls. A response without the field (an older server, or an endpoint that
// does not report it) must read as NO permissions, never as all of them.

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import { getMe, login, normalizeAuthUser, updateMe, type AuthUser } from './api';

const baseUser = {
  id: 'u1',
  username: 'alice',
  email: 'alice@example.test',
  roles: ['viewer'],
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
};

function respondWith(data: unknown) {
  global.fetch = vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => ({ data }),
  })) as unknown as typeof fetch;
}

describe('AuthUser permissions', () => {
  beforeEach(() => vi.restoreAllMocks());
  afterEach(() => vi.restoreAllMocks());

  it('keeps the permissions the server reported', () => {
    const u = normalizeAuthUser({ ...baseUser, permissions: ['library.view', 'collections.manage'] });
    expect(u.permissions).toEqual(['library.view', 'collections.manage']);
  });

  it('defaults an absent field to an empty list', () => {
    const u = normalizeAuthUser(baseUser as unknown as AuthUser);
    expect(u.permissions).toEqual([]);
  });

  it('ignores a malformed field instead of passing it on', () => {
    const u = normalizeAuthUser({ ...baseUser, permissions: 'collections.manage' } as unknown as AuthUser);
    expect(u.permissions).toEqual([]);
  });

  it('getMe returns the reported permissions', async () => {
    respondWith({ user: { ...baseUser, permissions: ['collections.manage'] } });
    expect((await getMe()).permissions).toEqual(['collections.manage']);
  });

  it('getMe fails closed against a server that does not report permissions', async () => {
    respondWith({ user: baseUser });
    expect((await getMe()).permissions).toEqual([]);
  });

  it('login carries the permissions and defaults when absent', async () => {
    respondWith({ user: { ...baseUser, permissions: ['library.view'] }, expires_at: 'x' });
    expect((await login({ username: 'alice', password: 'pw' })).user.permissions).toEqual(['library.view']);
    respondWith({ user: baseUser, expires_at: 'x' });
    expect((await login({ username: 'alice', password: 'pw' })).user.permissions).toEqual([]);
  });

  it('updateMe never yields an undefined permissions list', async () => {
    respondWith({ user: baseUser });
    expect((await updateMe({ email: 'a@example.test' })).permissions).toEqual([]);
  });
});
