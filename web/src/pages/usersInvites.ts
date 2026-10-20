// file: web/src/pages/usersInvites.ts
// version: 1.0.0
// guid: 1f6b3d82-7c45-4e9a-b2d0-8a5e9c3f7b61
// last-edited: 2026-10-19

// Helpers for the Users page invitation sections, kept out of Users.tsx so that
// file exports only components (react-refresh).

export interface AcceptedInvite {
  username: string;
  email?: string;
  accepted_at: string;
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
    default:
      return a.email
        ? { label: 'Cloudflare update pending', ok: false }
        : { label: 'No email: nothing to add', ok: true };
  }
}
