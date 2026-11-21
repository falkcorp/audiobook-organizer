// file: web/src/components/users/userAccountParts.ts
// version: 0.1.0
// guid: 419d37e0-b3e7-4cfd-a43f-b64c4d678230
// last-edited: 2026-11-21

// Shared pieces of the Users page and the account-merge extension (F9-04 slice 4): the row type, error text and the data-family labels.

import { describeRequestError } from '../../utils/apiResponse';

/** The part of a user row these components need. */
export interface MergeableUser {
  id: string;
  username: string;
  status: string;
  roles?: string[];
  merged_into_user_id?: string;
}

/**
 * The _system attribution account (and any account the server marks `system`). It cannot sign
 * in and only attributes background work, so it is never merged away and never a survivor. The
 * server refuses both; this keeps it out of the choices.
 */
export const isSystemAccount = (u: Pick<MergeableUser, 'username' | 'status'>) =>
  u.status === 'system' || u.username === '_system';

export const errorText = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : describeRequestError(err, fallback);

const FAMILY_LABELS: Record<string, string> = {
  book_state: 'Books with listening progress',
  positions: 'Playback positions',
  bookmarks: 'Bookmarks',
  oauth_identities: 'Linked sign-ins',
  api_keys: 'API keys',
};

export const familyLabel = (family: string) => FAMILY_LABELS[family] ?? family.replace(/_/g, ' ');
