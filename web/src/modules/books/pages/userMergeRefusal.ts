// file: web/src/modules/books/pages/userMergeRefusal.ts
// version: 0.1.0
// guid: f2af55b4-7d4d-4047-9166-2211a4ab6508
// last-edited: 2026-11-22

// F4-08 B10p: the codes the account-merge undo and resume routes refuse with,
// in words. The server answers 409 with a problem body ({error, code, status}) whose `code`
// names the first thing that no longer matches; the panel shows the sentence
// here instead of the code. A code with no sentence falls back to the server's
// own text, so a code added later still shows something.

import { ApiError } from '../../../services/api';

export const USER_MERGE_REFUSAL_TEXT: Readonly<Record<string, string>> = {
  // Core's reversal: whole or refused.
  not_merged:
    'The merged-away account is no longer marked as merged into the account that stayed, so there is nothing to undo.',
  source_changed:
    'The merged-away account was changed after the merge (it was re-enabled, edited or merged again), so it cannot be put back as it was.',
  source_deleted: 'The merged-away account has been deleted, so it cannot be brought back.',
  target_deleted:
    'The account the merge folded into has been deleted, so the merge cannot be taken back out of it.',
  target_inactive:
    'The account that stayed is disabled or not yet active. Enable it, then undo the merge.',
  target_merged:
    'The account that stayed has itself been merged into another account. Undo that merge first.',
  email_claimed:
    'Another account now holds the address the merged-away account had verified, so it cannot be given back.',
  key_moved:
    'An API key the merge moved has changed since (it was revoked, disabled, renewed or reassigned), so the merge can no longer be undone as a whole.',
  signin_moved:
    'A sign-in the merge moved has changed since (it was unlinked or linked elsewhere), so the merge can no longer be undone as a whole.',
  last_admin:
    'Undoing this merge would leave no active admin. Make another account an active admin first.',
  // Books' own.
  sweep_pending:
    'The merge did not finish moving the rows written while it ran. Run Resume on this merge first to finish that sweep, then undo it.',
  receipt_missing:
    'This merge was not recorded in enough detail to tell which sign-ins and keys it moved, so it cannot be undone.',
  conflict:
    'An account or a row changed while the undo was being prepared. Nothing was changed; try again.',
  merge_permanent:
    'This merge cannot be undone or resumed: it was made before the switch to the new account system, or it is already finished.',
};

/** The problem code an ApiError carries, or '' when there is none. */
export function userMergeRefusalCode(err: unknown): string {
  if (!(err instanceof ApiError)) return '';
  const code = (err.data as { code?: unknown } | undefined)?.code;
  return typeof code === 'string' ? code : '';
}

/**
 * What to tell the admin for a failed undo or resume: the sentence for the
 * refusal code when there is one, otherwise the server's own message.
 */
export function userMergeRefusalMessage(err: unknown, fallback: string): string {
  const code = userMergeRefusalCode(err);
  const text = USER_MERGE_REFUSAL_TEXT[code];
  if (text) return text;
  return err instanceof Error && err.message ? err.message : fallback;
}
