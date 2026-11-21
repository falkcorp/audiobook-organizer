// file: web/src/modules/books/layout/legacyAuth.ts
// version: 0.1.0
// guid: 0b5d8f31-9c64-4a27-8e1d-f3a7c2b9d406
// last-edited: 2026-11-21

import type { AuthState } from '@mediacore/shell';

/** The part of the legacy AuthContext value the layout reads. */
interface LegacyAuth {
  requiresAuth: boolean;
  user: { id: string; username: string; email: string } | null;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

/**
 * The legacy auth context in the shell's AuthState shape (status, user, logout,
 * refresh), for the shell user menu. A signed-in user exists only when the
 * server requires auth, as the old menu decided; every other state is
 * "anonymous" to it. F9-03 swaps the legacy provider for the shell's and
 * deletes this adapter.
 */
export function toShellAuth(legacy: LegacyAuth): Pick<AuthState, 'status' | 'user' | 'logout' | 'refresh'> {
  const signedIn = legacy.requiresAuth && legacy.user !== null;
  return {
    status: signedIn ? 'authenticated' : 'anonymous',
    user: signedIn ? legacy.user : null,
    logout: legacy.logout,
    refresh: legacy.refresh,
  };
}
