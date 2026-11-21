// file: web/src/modules/books/layout/legacyAuth.test.ts
// version: 0.1.0
// guid: 9e4b7a26-1d83-4f50-a6c2-b8f0d3e5c719
// last-edited: 2026-11-21

import { describe, expect, it, vi } from 'vitest';
import { toShellAuth } from './legacyAuth';

const user = { id: 'u1', username: 'batman', email: 'bruce@wayne.example' };
const logout = vi.fn();
const refresh = vi.fn();

describe('toShellAuth', () => {
  it('is authenticated, with the user and the same logout and refresh, when auth is required and a user exists', () => {
    expect(toShellAuth({ requiresAuth: true, user, logout, refresh })).toEqual({
      status: 'authenticated',
      user,
      logout,
      refresh,
    });
  });

  it.each([
    ['auth not required', { requiresAuth: false, user }],
    ['no user', { requiresAuth: true, user: null }],
    ['neither', { requiresAuth: false, user: null }],
  ])('is anonymous with no user when %s', (_label, legacy) => {
    const shell = toShellAuth({ ...legacy, logout, refresh });
    expect(shell.status).toBe('anonymous');
    expect(shell.user).toBeNull();
  });
});
