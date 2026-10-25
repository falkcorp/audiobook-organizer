// file: web/src/utils/permissionError.ts
// version: 1.0.0
// guid: 5e441a67-d40e-49c5-8349-7855239acc07
// last-edited: 2026-10-25

import { ApiError } from '../services/api';

/**
 * The text to show for a request the server refused for lack of permission
 * (403), or null when the failure is anything else so the caller keeps its own
 * message. `action` finishes "You don't have permission to ...". The server's
 * own wording (for example "permission denied: library.delete") follows, so the
 * person can tell an administrator exactly which permission is missing.
 */
export function describePermissionDenied(error: unknown, action: string): string | null {
  if (!(error instanceof ApiError) || error.status !== 403) return null;
  const detail = error.message.trim();
  return detail !== ''
    ? `You don't have permission to ${action} (${detail}).`
    : `You don't have permission to ${action}.`;
}
