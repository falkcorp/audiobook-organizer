// file: web/src/services/coreIdentity.parity.test.ts
// version: 0.1.0
// guid: d5b78529-eb40-455e-9372-c8ce497cd511
// last-edited: 2026-11-22

// SPA parity gaps on core's identity calls (F4-08 B10sq): a 422's text sits only in errors[]
// (G2), the Users page's Created column needs created_at (G6), and every list reads all its pages
// instead of the first 1000 (rows 9 and 14).

import { afterEach, describe, expect, it, vi } from 'vitest';
import { changePassword, listAdminUsers, listRoleNames, updateMe } from './coreIdentity';
import { ApiError } from './apiError';
import { coreJson, stubCoreFetch } from '../test/coreFetch';

afterEach(() => vi.unstubAllGlobals());

const invalid = (errors: { field: string; code: string; message: string }[]) =>
  new Response(
    JSON.stringify({
      type: 'https://mediacore.dev/problems/validation_failed',
      title: 'Unprocessable Entity',
      status: 422,
      code: 'validation_failed',
      request_id: 'r1',
      errors,
    }),
    { status: 422, headers: { 'Content-Type': 'application/problem+json' } }
  );

describe('a 422 validation_failed carries its text only in errors[] (G2)', () => {
  it('uses the first field message, not the bare status text', async () => {
    stubCoreFetch({
      'PATCH /me': invalid([
        { field: '/body/email', code: 'not_plain', message: 'use a plain ASCII address' },
        { field: '/body/other', code: 'too_long', message: 'second message' },
      ]),
    });
    const err = await updateMe({ email: 'selina@example.test' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).message).toBe('use a plain ASCII address');
    expect((err as ApiError).status).toBe(422);
  });

  it('skips a blank field message and finds the first one with text', async () => {
    stubCoreFetch({
      'PUT /me/password': invalid([
        { field: '/body/new', code: 'x', message: '   ' },
        { field: '/body/new', code: 'too_short', message: 'must be at least 8 characters' },
      ]),
    });
    await expect(changePassword({ current_password: 'a', new_password: 'b' })).rejects.toThrow(
      'must be at least 8 characters'
    );
  });

  it('keeps detail first when the problem has one', async () => {
    stubCoreFetch({
      'PATCH /me': new Response(
        JSON.stringify({
          title: 'Unprocessable Entity', status: 422, code: 'validation_failed',
          detail: 'the profile could not be saved',
          errors: [{ field: '/body/email', code: 'x', message: 'field text' }],
        }),
        { status: 422, headers: { 'Content-Type': 'application/problem+json' } }
      ),
    });
    await expect(updateMe({ email: 'x' })).rejects.toThrow('the profile could not be saved');
  });

  it('falls back to the title when there is neither detail nor field message', async () => {
    stubCoreFetch({ 'PATCH /me': invalid([]) });
    await expect(updateMe({ email: 'x' })).rejects.toThrow('Unprocessable Entity');
  });
});

describe('the account list (G6 and paging)', () => {
  const user = (n: number) => ({
    id: `u${n}`,
    username: `user.${n}`,
    email: '',
    roles: ['viewer'],
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
  });

  it('carries created_at through to the summary the Users page renders', async () => {
    stubCoreFetch({ 'GET /users': () => coreJson({ items: [user(1)], count: 1, limit: 1000, offset: 0, total: 1 }) });
    expect((await listAdminUsers())[0].created_at).toBe('2026-01-01T00:00:00Z');
  });

  it('reads every page until the total is reached, by offset', async () => {
    const all = Array.from({ length: 1500 }, (_, i) => user(i));
    const { calls } = stubCoreFetch({
      'GET /users': (call) => {
        const q = new URL(call.path, 'http://x').searchParams;
        const offset = Number(q.get('offset') ?? 0);
        const limit = Number(q.get('limit') ?? 50);
        const items = all.slice(offset, offset + limit);
        return coreJson({ items, count: items.length, limit, offset, total: all.length });
      },
    });
    const got = await listAdminUsers();
    expect(got).toHaveLength(1500);
    expect(got.map((u) => u.id)).toEqual(all.map((u) => u.id));
    expect(calls.map((c) => c.path)).toEqual(['/users?limit=1000&offset=0', '/users?limit=1000&offset=1000']);
  });

  it('stops on an empty page even when the total says there is more', async () => {
    const { calls } = stubCoreFetch({
      'GET /users': (call) =>
        call.path.includes('offset=0')
          ? coreJson({ items: [user(1)], count: 1, limit: 1000, offset: 0, total: 5 })
          : coreJson({ items: null, count: 0, limit: 1000, offset: 1, total: 5 }),
    });
    expect(await listAdminUsers()).toHaveLength(1);
    expect(calls).toHaveLength(2);
  });

  it('fails the whole list when a later page fails, instead of returning a partial list', async () => {
    stubCoreFetch({
      'GET /users': (call) =>
        call.path.includes('offset=0')
          ? coreJson({ items: Array.from({ length: 1000 }, (_, i) => user(i)), count: 1000, limit: 1000, offset: 0, total: 1500 })
          : new Response(JSON.stringify({ title: 'the store is down', status: 500, code: 'internal' }), {
              status: 500,
              headers: { 'Content-Type': 'application/problem+json' },
            }),
    });
    await expect(listAdminUsers()).rejects.toThrow('the store is down');
  });
});

describe('the role list pages too', () => {
  it('reads every page of roles', async () => {
    const roles = Array.from({ length: 1200 }, (_, i) => ({ name: `role-${i}` }));
    stubCoreFetch({
      'GET /roles': (call) => {
        const q = new URL(call.path, 'http://x').searchParams;
        const offset = Number(q.get('offset') ?? 0);
        const limit = Number(q.get('limit') ?? 50);
        const items = roles.slice(offset, offset + limit);
        return coreJson({ items, count: items.length, limit, offset, total: roles.length });
      },
    });
    expect(await listRoleNames()).toHaveLength(1200);
  });
});
