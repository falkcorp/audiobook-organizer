// file: web/src/services/booksInvites.ts
// version: 0.1.0
// guid: 74858d3d-5e5f-4a42-9cbb-f98d930dfbb6
// last-edited: 2026-11-22

// The pending-invite list and revoke on the BOOKS routes (GET /api/v1/users/invites and
// DELETE /api/v1/users/invites/:id), not core's native invites route.
//
// Why: HEAD admitted both routes to users.manage by any method, and with authentication off to
// every request; books keeps that reach under core identity (F4-08 B10y). Core's native list and
// delete need a signed-in user, so under auth-off they answer 401 authentication_required, and
// that code ends the session in the shared client (SESSION_ENDING_CODES). The invite list must
// keep working with auth off, so it stays on the route that does.
//
// One list call also carries what the Users page shows beside the invites: the accepted invites
// and the mail and guest-policy flags (HEAD's single response). Under core identity `accepted` is
// empty (the accept is core's) and a pending invite carries no token: core keeps none.
// The row type has no token field on purpose: the response may carry one for a caller allowed
// to see it, and it is dropped here so no link secret ever reaches component state.

import { apiFetch } from '../utils/apiFetch';
import { unwrapData } from '../utils/apiResponse';
import { apiErrorFromResponse } from '../utils/signInRequired';
import type { AcceptedInvite } from '../components/users/usersInvites';

const INVITES = '/api/v1/users/invites';

/** One pending invite as books lists it (HEAD's pendingInviteView, minus the token). */
export interface PendingInviteRow {
  /** Non-secret handle; revoke and resend take this. */
  id: string;
  username: string;
  email?: string;
  role_id: string;
  expires_at: string;
  /** When the invitation email went out; absent until one has. */
  email_sent_at?: string;
}

export interface InviteList {
  invites: PendingInviteRow[];
  accepted: AcceptedInvite[];
  mailConfigured: boolean;
  /** Why an invitation email cannot go out, when it cannot. */
  mailUnavailableReason: string;
  guestPolicySyncConfigured: boolean;
}

interface InviteListBody {
  invites?: (PendingInviteRow & { token?: string })[];
  accepted?: AcceptedInvite[];
  mail_configured?: boolean;
  mail_unavailable_reason?: string;
  guest_policy_sync_configured?: boolean;
}

export async function listInvites(): Promise<InviteList> {
  const resp = await apiFetch(INVITES);
  if (!resp.ok) throw await apiErrorFromResponse(resp, `HTTP ${resp.status}`);
  const b = unwrapData<InviteListBody>(await resp.json());
  return {
    invites: (b.invites ?? []).map((i) => ({
      id: i.id,
      username: i.username,
      ...(i.email ? { email: i.email } : {}),
      role_id: i.role_id,
      expires_at: i.expires_at,
      ...(i.email_sent_at ? { email_sent_at: i.email_sent_at } : {}),
    })),
    accepted: b.accepted ?? [],
    mailConfigured: !!b.mail_configured,
    mailUnavailableReason: b.mail_unavailable_reason ?? '',
    guestPolicySyncConfigured: !!b.guest_policy_sync_configured,
  };
}

/** Revokes a pending invite by its id (never its token). */
export async function deleteInvite(id: string): Promise<void> {
  const resp = await apiFetch(`${INVITES}/${encodeURIComponent(id)}`, { method: 'DELETE' });
  if (!resp.ok) throw await apiErrorFromResponse(resp, `HTTP ${resp.status}`);
}
