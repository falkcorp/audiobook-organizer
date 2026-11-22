// file: web/src/services/api.authUser.test.ts
// version: 1.0.0
// guid: 4c8e1f27-9a35-4d60-b7e2-0f3a5d9c6b18
// last-edited: 2026-10-18
//
// AuthUser carries the caller's effective permissions so the UI can hide write
// controls. Core's GET /me lists them as a sibling of `user`; a response without
// the list (or with a malformed one) must read as NO permissions, never as all of them.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { getAuthStatus, getMe, login, normalizeAuthUser, updateMe, type AuthUser } from './api';
import { BOOKS_PERMISSIONS } from '../generated/permissions';
import { coreJson, stubCoreFetch } from '../test/coreFetch';

const coreUser = {
  id: 'u1',
  username: 'bruce.wayne',
  email: 'bruce@example.test',
  roles: ['viewer'],
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
};

const session = { public_id: 's1', expires_at: '2026-01-02T00:00:00Z' };

afterEach(() => vi.unstubAllGlobals());

describe('AuthUser permissions', () => {
  it('keeps the permissions the server reported', () => {
    const user = { ...coreUser, created_at: 'x', permissions: [BOOKS_PERMISSIONS.VIEW] };
    expect(normalizeAuthUser(user).permissions).toEqual([BOOKS_PERMISSIONS.VIEW]);
  });

  it('defaults an absent field to an empty list', () => {
    expect(normalizeAuthUser(coreUser as unknown as AuthUser).permissions).toEqual([]);
  });

  it('ignores a malformed field instead of passing it on', () => {
    const bad = { ...coreUser, permissions: 'books:view' } as unknown as AuthUser;
    expect(normalizeAuthUser(bad).permissions).toEqual([]);
  });

  it('getMe returns the permissions /me lists', async () => {
    stubCoreFetch({
      'GET /me': coreJson({ user: coreUser, permissions: [BOOKS_PERMISSIONS.COLLECTIONS_MANAGE] }),
    });
    const me = await getMe();
    expect(me.permissions).toEqual([BOOKS_PERMISSIONS.COLLECTIONS_MANAGE]);
    expect(me.username).toBe('bruce.wayne');
  });

  it('getMe fails closed against a /me that lists no permissions', async () => {
    stubCoreFetch({ 'GET /me': coreJson({ user: coreUser }) });
    expect((await getMe()).permissions).toEqual([]);
  });

  it('login opens the session, then reads the permissions of the new session from /me', async () => {
    const { calls } = stubCoreFetch({
      'POST /auth/login': coreJson(session),
      'GET /me': coreJson({ user: coreUser, permissions: [BOOKS_PERMISSIONS.VIEW] }),
    });
    const out = await login({ username: 'bruce.wayne', password: 'pw', remember_me: true });
    expect(out.user.permissions).toEqual([BOOKS_PERMISSIONS.VIEW]);
    expect(calls[0]).toMatchObject({
      method: 'POST',
      path: '/auth/login',
      body: { username: 'bruce.wayne', password: 'pw', remember: true },
    });
  });

  it('updateMe never yields an undefined permissions list', async () => {
    stubCoreFetch({
      'PATCH /me': coreJson(coreUser),
      'GET /me': coreJson({ user: coreUser }),
    });
    expect((await updateMe({ email: 'a@example.test' })).permissions).toEqual([]);
  });
});

describe('getAuthStatus', () => {
  const status = (body: object) => stubCoreFetch({ 'GET /auth/status': () => coreJson(body) });

  it('requires sign-in whenever auth is on, including before the first account, and offers setup then', async () => {
    status({ auth_disabled: false, has_users: false, setup_required: true });
    expect(await getAuthStatus()).toEqual({
      has_users: false,
      auth_enabled: true,
      requires_auth: true,
      bootstrap_ready: true,
    });
  });

  it('requires sign-in and offers no setup once an account exists', async () => {
    status({ auth_disabled: false, has_users: true, setup_required: false });
    expect(await getAuthStatus()).toMatchObject({ requires_auth: true, bootstrap_ready: false });
  });

  it('requires nothing when auth is disabled', async () => {
    status({ auth_disabled: true, has_users: true, setup_required: false });
    expect(await getAuthStatus()).toMatchObject({ auth_enabled: false, requires_auth: false });
  });
});
