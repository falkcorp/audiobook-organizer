// file: internal/server/invite_accept_identity.go
// version: 0.1.0
// guid: 8e3c5a27-1f64-4d90-b2a8-6c70d419e3f5
// last-edited: 2026-11-22

package server

import (
	"context"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// recordInviteAccept writes books' own record of an accepted invite
// (database.InviteAccept) from core's InviteAccepted event. It is the first
// concern of the account lifecycle's InviteAccepted reaction (AccountLifecycle),
// ahead of and independent of the guest-policy sync and the guest key: it runs
// for an invite that named no address (guestOnInviteAccepted returns before it
// records anything for that), and nothing the sync does or fails at can reach it.
//
// What each member is, and where it comes from, with nothing guessed:
//
//   - invite id, user id, address, and what the accept did with the address
//     (EmailBinding, SSOGrant) come from the event. The binding and the grant are
//     HEAD's own four-way decision (planInviteAcceptLocked), restated over the
//     event's flags: no address at all leaves both empty; an address the invitation
//     was not mailed to is "not_emailed" with no grant; one mailed by a creator who
//     no longer held admin is "creator_not_admin" for both; and one that was bound
//     is "bound" and "granted". The event documents the last as exactly
//     BindEmailOnAccept AND CreatorHeldAdmin.
//   - the displaced accounts are the event's DisplacedUserIDs.
//   - the username and the accept time are the event's own (Username, AcceptedAt):
//     core stamps them in the accept's batch, the account row is written with the
//     same two values, and the accept time is the one reading of core's clock the
//     accept's audit record and session carry. Neither is the time books' callback
//     happens to run, so a delayed sync, or a delayed callback, moves neither, and
//     nothing is read back from core to get them, so no read can fail and cost the
//     accept its row.
//
// A row is refused (never written with a made-up value) only if the store cannot
// keep one or the event somehow carries no accept time; both are logged with no
// value in the line.
func (s *Server) recordInviteAccept(ctx context.Context, e coreauth.InviteAccepted) {
	st, ok := database.AsCapability[database.InviteAcceptStore](s.storeForWiring())
	if !ok {
		inviteAccessLog.Warn("invite accept: the store cannot keep accepted-invite rows, so the Users page will not list this accept")
		return
	}
	a := database.InviteAccept{
		InviteID:   e.InviteID,
		UserID:     string(e.UserID),
		Username:   e.Username,
		Email:      coreauth.NormalizeEmail(e.Email),
		AcceptedAt: e.AcceptedAt,
	}
	a.EmailBinding, a.SSOGrant = inviteAcceptBinding(a.Email, e.BindEmailOnAccept, e.CreatorHeldAdmin)
	for _, id := range e.DisplacedUserIDs {
		a.DisplacedUserIDs = append(a.DisplacedUserIDs, string(id))
	}
	if err := st.RecordInviteAccept(a); err != nil {
		inviteAccessLog.Warn("invite accept: recording the accepted-invite row for user %s failed: %v", logger.SanitizeLogValue(a.UserID), err)
	}
}

// inviteAcceptBinding is HEAD's EmailBinding and SSOGrant for an accept of an invite
// that named email, from the two facts the accept turned on (the invite's flag as
// stored, and whether its creator still held admin). Both are "" for an invite with
// no address, as HEAD's were.
func inviteAcceptBinding(email string, mailed, creatorHeldAdmin bool) (binding, grant string) {
	switch {
	case email == "":
		return "", ""
	case !mailed:
		return database.InviteEmailNotEmailed, ""
	case !creatorHeldAdmin:
		return database.InviteEmailCreatorNotAdmin, database.InviteSSOCreatorNotAdmin
	default:
		return database.InviteEmailBound, database.InviteSSOGranted
	}
}
