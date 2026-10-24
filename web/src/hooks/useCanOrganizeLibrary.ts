// file: web/src/hooks/useCanOrganizeLibrary.ts
// version: 1.0.0
// guid: 8e2c4b17-5a39-4d60-b1f8-3c70d9a2e564
// last-edited: 2026-10-23

import { useOptionalAuth } from '../contexts/AuthContext';

/** The permission the server's library-organize routes (for example a retired-version restore) require. */
export const LIBRARY_ORGANIZE_PERMISSION = 'library.organize';

/**
 * Whether the caller may run library-organize actions. Advisory (every route
 * enforces its own permission) and fail-closed, like useCanManageCollections:
 * outside an AuthProvider, until auth has initialised, when the server's auth
 * setting is unknown, when authentication is on but nobody is signed in, and
 * when the permission list is missing or empty, the answer is no. The one open
 * case is authentication explicitly OFF (auth_enabled === false), where the
 * server's permission gate is a no-op and hiding the control would lock a
 * single-user install out of its own page.
 */
export function useCanOrganizeLibrary(): boolean {
  const auth = useOptionalAuth();
  if (!auth || !auth.initialized) return false;
  if (auth.authEnabled === false) return true;
  return auth.user?.permissions?.includes(LIBRARY_ORGANIZE_PERMISSION) ?? false;
}
