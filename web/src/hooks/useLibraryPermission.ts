// file: web/src/hooks/useLibraryPermission.ts
// version: 1.0.0
// guid: 1b4c472c-399a-43a3-8df5-25a5dbd4979e
// last-edited: 2026-10-25

import { useOptionalAuth } from '../contexts/AuthContext';

/** Moving books to the trash (the server asks for it on every route that does). */
export const LIBRARY_DELETE_PERMISSION = 'library.delete';
/** Taking books out of the trash. */
export const LIBRARY_ORGANIZE_PERMISSION = 'library.organize';

/** The slice of the auth context a permission decision reads. */
export interface PermissionAuth {
  initialized: boolean;
  authEnabled: boolean | null;
  user: { permissions?: string[] } | null;
}

/**
 * Whether the caller holds `permission`, for hiding or disabling a control the
 * server would refuse. Advisory only (every route enforces its own permission)
 * and fail-closed, like useCanManageCollections: until auth has initialised,
 * when the server's auth setting is unknown, and when authentication is on but
 * nobody is signed in or the permission list is missing, the caller does not
 * hold it. The one open case is authentication explicitly OFF, where the
 * server's gate is a no-op and every caller may do everything. With no auth
 * information at all (`null`: the component renders outside an AuthProvider)
 * the control is left as it was, since nothing says it would be refused.
 */
export function holdsPermission(auth: PermissionAuth | null, permission: string): boolean {
  if (auth === null) return true;
  if (!auth.initialized) return false;
  if (auth.authEnabled === false) return true;
  return auth.user?.permissions?.includes(permission) ?? false;
}

export function useHasPermission(permission: string): boolean {
  return holdsPermission(useOptionalAuth(), permission);
}
