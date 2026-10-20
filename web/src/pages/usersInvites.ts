// file: web/src/pages/usersInvites.ts
// version: 1.1.0
// guid: 1f6b3d82-7c45-4e9a-b2d0-8a5e9c3f7b61
// last-edited: 2026-10-19

// Helpers for the Users page invitation sections, kept out of Users.tsx so that
// file exports only components (react-refresh).

export interface AcceptedInvite {
  username: string;
  email?: string;
  accepted_at: string;
  /** "bound" or "not_emailed" (the email was never delivered, so not verified). */
  email_binding?: string;
  /** "granted" or "creator_not_admin". */
  sso_grant?: string;
  guest_policy_status?: string;
  guest_policy_detail?: string;
}

/** The link an invitee opens; the page is served from this same origin. */
export function inviteLink(token: string): string {
  return `${window.location.origin}/invite/${token}`;
}

/**
 * The admin-facing label for the Cloudflare guest-policy outcome of an
 * accepted invite. Anything that is not a confirmed add tells the admin to add
 * the email to the Access policy by hand.
 */
export function guestPolicyLabel(a: AcceptedInvite): { label: string; ok: boolean } {
  switch (a.guest_policy_status) {
    case 'added':
      return { label: 'Added to Cloudflare guest policy', ok: true };
    case 'already_present':
      return { label: 'Already in Cloudflare guest policy', ok: true };
    case 'failed':
      return { label: 'Cloudflare update failed: add manually', ok: false };
    case 'not_configured':
      return { label: 'Not automated: add to Cloudflare manually', ok: false };
    case 'skipped':
      return a.sso_grant === 'creator_not_admin'
        ? { label: 'No access granted: invited by a non-admin', ok: false }
        : { label: 'Email not verified: not granted', ok: false };
    default:
      return a.email
        ? { label: 'Cloudflare update pending', ok: false }
        : { label: 'No email: nothing to add', ok: true };
  }
}

/** What happened to the invite's email address on accept, for the admin. */
export function emailBindingLabel(a: AcceptedInvite): string {
  if (!a.email) return '—';
  if (a.email_binding === 'not_emailed') {
    return `${a.email} (not verified: the link was not emailed, so it was not set on the account)`;
  }
  return a.email;
}
