// file: internal/server/guest_policy_identity.go
// version: 0.1.0
// guid: e91b3c64-2f08-4a7d-8c15-6d4a09b7f352
// last-edited: 2026-11-22

package server

import (
	"context"
	"errors"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// The Cloudflare Access guest policy under core identity (F4-08 B10r).
//
// Under core identity the accounts and the invites are core's, so HEAD's call
// sites for the guest-policy sync (the books accept handler, the books user
// handlers' deactivate and e-mail routes) no longer see an account change. Core
// reports each one after it commits through auth.Lifecycle, and books registers
// the one hook below. What each event does is what HEAD did at the matching call
// site, over the same sync and revoke code:
//
//	InviteAccepted                  startGuestPolicySync (auth_accept_invite.go)
//	StatusChanged to disabled       revokeGuestAccess "user deactivated" (handlers/user.go)
//	EmailChanged                    revokeGuestAccess "email changed" / "...by an admin"
//	UserDeleted, UserMergedAway     none at HEAD (a deleted or merged account kept its
//	                                Cloudflare entry); core's two new events remove it
//
// Failure behaves as it did at HEAD: a callback never fails the account event it
// follows (core calls it after the commit and recovers a panic), a Cloudflare
// error is logged and, for an accept, recorded on the invite's outcome row, and
// the keep rules (owner, Access allow-list, another verified holder) are the same.
//
// The bookkeeping HEAD kept on the invite and on the inviteallow: grant is books'
// own here (store.GuestPolicyStore): core has neither field, and inviteallow: is
// the SSO grant the identity cutover consumed.

// GuestPolicyLifecycle is the auth.Lifecycle books registers with core when it
// runs under core identity. Every field is set: an event books does not act on
// would be a hole a later core change walks through unnoticed.
func (s *Server) GuestPolicyLifecycle() coreauth.Lifecycle {
	return coreauth.Lifecycle{
		InviteAccepted: s.guestOnInviteAccepted,
		StatusChanged:  s.guestOnStatusChanged,
		EmailChanged:   s.guestOnEmailChanged,
		UserDeleted:    s.guestOnUserDeleted,
		UserMergedAway: s.guestOnUserMergedAway,
	}
}

// guestGrantStore is the bookkeeping the sync and the revoke run over: books' own
// guestpolicy: rows under core identity, HEAD's inviteallow: grants otherwise.
func (s *Server) guestGrantStore() (database.InviteEmailGrantStore, bool) {
	if s.identity != nil {
		gp, ok := database.AsCapability[database.GuestPolicyStore](s.storeForWiring())
		if !ok {
			return nil, false
		}
		return gp.GuestPolicyGrants(), true
	}
	return database.AsCapability[database.InviteEmailGrantStore](s.storeForWiring())
}

// guestOutcomeRecorder is the record func of syncGuestPolicy for a core invite:
// HEAD wrote the outcome onto the invite, which is core's now, so it goes to a
// row of books' own keyed by the invite's id. The admin screens that show it are
// B10t-books' to wire; the row is complete from here.
func (s *Server) guestOutcomeRecorder(inviteID, userID string) func(status, detail string) {
	return func(status, detail string) {
		gp, ok := database.AsCapability[database.GuestPolicyStore](s.storeForWiring())
		if !ok {
			return
		}
		if err := gp.RecordGuestPolicyOutcome(inviteID, userID, status, detail, time.Now()); err != nil {
			inviteAccessLog.Warn("invite accept: recording the guest-policy outcome failed: %v", err)
		}
	}
}

// guestOnInviteAccepted is startGuestPolicySync over a core accept. HEAD's gates,
// in HEAD's order, from the event's facts:
//
//   - no address on the invite: nothing, as HEAD (it returned silently);
//   - not mailed to the address (BindEmailOnAccept false): skipped, with HEAD's reason;
//   - mailed, but its creator did not hold admin at the accept (CreatorHeldAdmin
//     false): skipped, with HEAD's reason;
//   - otherwise the guest is granted: the address goes onto the guest policy in
//     the background.
//
// The bookkeeping row is written here, in the callback, not in the accept's batch
// (core's batch is core's). A disable or e-mail change can therefore land between
// core's commit and that write, and its revoke would find no row to act on. So the
// account is read back from core after the write: one that no longer stands (gone,
// not active, a different address) is a revoked access, and nothing is added. A
// revoke after the write finds the row and takes the same arbitration HEAD's did
// (syncGuestPolicy: SetInviteEmailGrantCFAdded against DeleteInviteEmailGrant
// under one store lock).
func (s *Server) guestOnInviteAccepted(ctx context.Context, e coreauth.InviteAccepted) {
	email := coreauth.NormalizeEmail(e.Email)
	if email == "" {
		return
	}
	userID := string(e.UserID)
	record := s.guestOutcomeRecorder(e.InviteID, userID)
	switch {
	case !e.BindEmailOnAccept:
		record(database.InviteGuestPolicySkipped, "the invitation was not emailed to this address, so the address was not verified; set the user's email and add it to Cloudflare by hand if wanted")
		return
	case !e.CreatorHeldAdmin:
		record(database.InviteGuestPolicySkipped, "the invitation was created by someone without the admin role, so no SSO or Cloudflare access was granted")
		return
	}
	client, err := s.guestPolicyClient()
	if err != nil {
		inviteAccessLog.Warn("invite accept: guest-policy settings are invalid, add the email by hand: %v", err)
		record(database.InviteGuestPolicyFailed, err.Error())
		return
	}
	if client == nil {
		record(database.InviteGuestPolicyNotConfigured, "")
		return
	}
	gp, ok := database.AsCapability[database.GuestPolicyStore](s.storeForWiring())
	if !ok {
		record(database.InviteGuestPolicyFailed, "the store cannot track guest-policy entries, so the Cloudflare entry could not be managed; add it by hand")
		return
	}
	if err := gp.PutGuestPolicyGrant(database.GuestPolicyGrant{Email: email, UserID: userID, InviteID: e.InviteID, GrantedAt: time.Now()}); err != nil {
		inviteAccessLog.Warn("invite accept: recording the guest-policy entry failed: %v", err)
		record(database.InviteGuestPolicyFailed, "the guest-policy entry could not be recorded, so Cloudflare was not updated: "+err.Error())
		return
	}
	stands, err := s.coreAccountStands(ctx, userID, email)
	if err != nil || !stands {
		// An unreadable account is treated as not standing: a failed add is
		// fixed by hand, a stale one widens access.
		if _, derr := gp.GuestPolicyGrants().DeleteInviteEmailGrant(email, userID); derr != nil {
			inviteAccessLog.Warn("invite accept: removing the guest-policy entry of an account that no longer stands failed: %v", derr)
		}
		if err != nil {
			inviteAccessLog.Warn("invite accept: the account could not be read back, so Cloudflare was not updated: %v", err)
			record(database.InviteGuestPolicyFailed, "the account could not be read back after the accept, so Cloudflare was not updated; add the address by hand if wanted")
			return
		}
		record(database.InviteGuestPolicyRevoked, "the access was revoked before Cloudflare was updated; nothing was added")
		return
	}
	parent := s.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	grants := gp.GuestPolicyGrants()
	s.bgWG.Go("invite-guest-policy-sync", func() {
		s.syncGuestPolicy(parent, client, grants, record, email, userID)
	})
}

// guestOnStatusChanged is HEAD's "user deactivated" revoke. A re-enable, or a
// move to a status that does not end access to the address, does nothing: HEAD's
// revoke ran only on the deactivation.
func (s *Server) guestOnStatusChanged(ctx context.Context, e coreauth.StatusChanged) {
	if e.To != coreauth.StatusDisabled {
		return
	}
	s.revokeGuestAccessCtx(ctx, string(e.UserID), e.Email, "user deactivated")
}

// guestOnEmailChanged is HEAD's "email changed" revoke, for the address the
// account gave up. Core fires it for a self-service change and an admin's alike
// (HEAD named the two reasons separately; the log line no longer can).
func (s *Server) guestOnEmailChanged(ctx context.Context, e coreauth.EmailChanged) {
	s.revokeGuestAccessCtx(ctx, string(e.UserID), e.From, "email changed")
}

// guestOnUserDeleted removes the entry of an account that is gone. HEAD never did:
// a deleted guest kept their Cloudflare entry.
func (s *Server) guestOnUserDeleted(ctx context.Context, e coreauth.UserDeleted) {
	s.revokeGuestAccessCtx(ctx, string(e.UserID), e.Email, "user deleted")
}

// guestOnUserMergedAway removes the entry of a merged-away source account: it is
// disabled for good and its address was cleared by the merge. HEAD never did.
func (s *Server) guestOnUserMergedAway(ctx context.Context, e coreauth.UserMergedAway) {
	s.revokeGuestAccessCtx(ctx, string(e.UserID), e.Email, "user merged away")
}

// errNoIdentityUsers is coreAccountStands' answer when the server was built
// under identity without core's identity view.
var errNoIdentityUsers = errors.New("the server has no view of core's accounts")

// coreAccountStands reports whether core still holds userID as an active account
// with exactly email (by SameEmail): the facts the guest entry rests on. It reads
// core's identity view, an actor-free read. An error is returned, never
// guessed past.
func (s *Server) coreAccountStands(ctx context.Context, userID, email string) (bool, error) {
	if s.identity == nil || s.identity.Users == nil {
		return false, errNoIdentityUsers
	}
	for u, err := range s.identity.Users.Users(ctx, coreauth.UserFilter{Status: coreauth.StatusActive}) {
		if err != nil {
			return false, err
		}
		if string(u.ID) == userID {
			return coreauth.SameEmail(u.Email, email), nil
		}
	}
	return false, nil
}

// keepForCoreHolder is the verified-holder step of keepGuestPolicyEmail under core
// identity: it keeps the entry while another ACTIVE account holds the address
// VERIFIED. It reads core's identity view (Identities.Users), which needs no actor:
// the filter is status, the result is read-only and nothing here acts for a person.
//
// HEAD asked the books store (GetUserByVerifiedEmail) and looked at one holder; the
// same rule over core is every active account other than userID whose address is
// the same by SameEmail and is marked verified. An unverified match counts for
// nothing (an e-mail typed into a profile proves nothing, and counting it would
// let anyone pin an address in the policy). A failed read, or no view to read,
// keeps the entry, as HEAD's failed lookup did.
func (s *Server) keepForCoreHolder(ctx context.Context, userID, email string) string {
	const unreadable = "the verified-email lookup failed, so another verified holder cannot be ruled out"
	if s.identity.Users == nil {
		return unreadable
	}
	for u, err := range s.identity.Users.Users(ctx, coreauth.UserFilter{Status: coreauth.StatusActive}) {
		if err != nil {
			return unreadable
		}
		if string(u.ID) != userID && u.EmailVerified && coreauth.SameEmail(u.Email, email) {
			return "another active user has this as their verified email"
		}
	}
	return ""
}
