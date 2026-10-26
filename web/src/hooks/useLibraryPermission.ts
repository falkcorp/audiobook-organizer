// file: web/src/hooks/useLibraryPermission.ts
// version: 1.2.0
// guid: 1b4c472c-399a-43a3-8df5-25a5dbd4979e
// last-edited: 2026-10-26

import { useOptionalAuth } from '../contexts/AuthContext';

/** Moving books to the trash (the server asks for it on every route that does). */
export const LIBRARY_DELETE_PERMISSION = 'library.delete';
/** Taking books out of the trash. */
export const LIBRARY_ORGANIZE_PERMISSION = 'library.organize';

/** The slice of the auth context a permission decision reads. */
export interface PermissionAuth {
  initialized: boolean;
  authEnabled: boolean | null;
  /** Authentication is on AND at least one user exists (/auth/status requires_auth). */
  requiresAuth: boolean;
  user: { permissions?: string[] } | null;
}

/**
 * Whether the caller holds `permission`, for hiding or disabling a control the
 * server would refuse. Advisory only (every route enforces its own permission)
 * and fail-closed: until auth has initialised,
 * when the server's auth setting is unknown, and when authentication is on but
 * nobody is signed in or the permission list is missing, the caller does not
 * hold it. Two cases are open because the server lets the caller through: when
 * authentication is explicitly OFF (its gate is a no-op), and on a first run
 * (authentication on, no user created yet: `requiresAuth` is false although
 * `authEnabled` is true), where the server's permission checks hold every
 * permission until the first user exists (middleware.HoldsPermission). With no
 * auth information at all (`null`: the component renders outside an
 * AuthProvider) the control is left as it was, since nothing says it would be
 * refused.
 */
export function holdsPermission(auth: PermissionAuth | null, permission: string): boolean {
  if (auth === null) return true;
  if (!auth.initialized) return false;
  if (auth.authEnabled === false) return true;
  if (auth.authEnabled === true && !auth.requiresAuth) return true;
  return auth.user?.permissions?.includes(permission) ?? false;
}

export function useHasPermission(permission: string): boolean {
  return holdsPermission(useOptionalAuth(), permission);
}
