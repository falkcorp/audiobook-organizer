// file: web/src/components/phoneKeyLabels.ts
// version: 1.0.0
// guid: 7b2d9e4c-1a63-4f58-9c0e-5d8a3b6f2e17
// last-edited: 2026-10-23

// Types and labels for the admin "Phone app keys" panel, kept out of the
// component file so that file exports only components (react-refresh).

/** One row of GET /api/v1/admin/guest-service-tokens. No secret, hash or client id. */
export interface GuestPhoneKey {
  user_id: string;
  username?: string;
  token_id?: string;
  status: string;
  detail?: string;
  created_at: string;
  revoked_at?: string;
  revoke_reason?: string;
  /** What Cloudflare reports; absent when it did not say. */
  last_seen_at?: string;
}

export interface GuestPhoneKeysBody {
  /** Switched on and the Cloudflare settings it needs are present. */
  enabled?: boolean;
  /** Switched on, whether or not the settings are complete. */
  flag_on?: boolean;
  last_seen_note?: string;
  tokens?: GuestPhoneKey[];
}

/** The admin-facing label for a key's lifecycle status. */
export function phoneKeyStatusLabel(status: string): {
  label: string;
  color: 'success' | 'warning' | 'default' | 'error';
} {
  switch (status) {
    case 'active':
      return { label: 'Active', color: 'success' };
    case 'pending':
    case 'minted':
      return { label: 'Being set up', color: 'warning' };
    case 'revoking':
      return { label: 'Revoking', color: 'warning' };
    case 'revoked':
      return { label: 'Revoked', color: 'default' };
    case 'failed':
      return { label: 'Setup failed', color: 'error' };
    case 'revoke_failed':
      return { label: 'Revoke failed: retry', color: 'error' };
    default:
      return { label: status, color: 'default' };
  }
}

/** Whether the admin can still usefully revoke this key. */
export function phoneKeyRevocable(status: string): boolean {
  return status !== 'revoked';
}
