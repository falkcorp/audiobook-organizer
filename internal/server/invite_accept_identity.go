// file: internal/server/invite_accept_identity.go
// version: 0.1.0
// guid: 8e3c5a27-1f64-4d90-b2a8-6c70d419e3f5
// last-edited: 2026-11-22

package server

import (
	"context"
	"errors"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// errNoAcceptedAccount is why an accept could not be recorded: core's identity
// view did not hand back the account the accept created.
var errNoAcceptedAccount = errors.New("core's identity view has no account for the accept")

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
//   - the username and the accept time are NOT on the event. Both are read from the
//     account the accept created, through core's actor-free identity view:
//     core builds that account in the accept's own batch with CreatedAt set to the
//     accept's clock reading (the one it audits and issues the session with), and
//     the username it was created under. Neither is the time books' callback
//     happens to run, so a delayed sync, or a delayed callback, moves neither.
//
// When the account cannot be read (no view, a storage fault, or an account that was
// deleted in the instants between core's commit and this call) there is no real
// accept time to record, and none is made up: the accept is logged, with no value
// in the line, and no row is written. The event is the gap: a core InviteAccepted
// that carried the accept time and the username would remove this read.
func (s *Server) recordInviteAccept(ctx context.Context, e coreauth.InviteAccepted) {
	st, ok := database.AsCapability[database.InviteAcceptStore](s.storeForWiring())
	if !ok {
		inviteAccessLog.Warn("invite accept: the store cannot keep accepted-invite rows, so the Users page will not list this accept")
		return
	}
	u, err := s.acceptedAccount(ctx, e.UserID)
	if err != nil {
		inviteAccessLog.Warn("invite accept: the accepted account could not be read, so the Users page will not list this accept: %v", err)
		return
	}
	a := database.InviteAccept{
		InviteID:   e.InviteID,
		UserID:     string(e.UserID),
		Username:   u.Username,
		Email:      coreauth.NormalizeEmail(e.Email),
		AcceptedAt: u.CreatedAt,
	}
	a.EmailBinding, a.SSOGrant = inviteAcceptBinding(a.Email, e.BindEmailOnAccept, e.CreatorHeldAdmin)
	for _, id := range e.DisplacedUserIDs {
		a.DisplacedUserIDs = append(a.DisplacedUserIDs, string(id))
	}
	if err := st.RecordInviteAccept(a); err != nil {
		inviteAccessLog.Warn("invite accept: recording the accepted-invite row for user %s failed: %v", logger.SanitizeLogValue(a.UserID), err)
	}
}

// acceptedAccount reads the account an accept created from core's identity view.
func (s *Server) acceptedAccount(ctx context.Context, id coreauth.UserID) (*coreauth.User, error) {
	if s.identity == nil || s.identity.Users == nil {
		return nil, errNoIdentityUsers
	}
	u, err := s.identity.Users.User(ctx, id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, errNoAcceptedAccount
	}
	return u, nil
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
