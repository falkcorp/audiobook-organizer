// file: internal/server/account_lifecycle.go
// version: 0.1.0
// guid: 4b7e2d19-6a03-4f58-b1c6-92d0e5a8f347
// last-edited: 2026-11-22

package server

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// AccountLifecycle is the ONE auth.Lifecycle books registers with core under
// core identity (F4-08 B10r + B10s). Core allows a single registration, so every
// books reaction to an account event lives behind this value, and two concerns
// run for each event that touches them:
//
//	guest policy      the Cloudflare Access guest policy (guest_policy_identity.go)
//	guest phone key   the guest's one-time phone-app key and its Cloudflare
//	                  service token (guest_key_claim.go, guest_service_tokens.go)
//
// Event by event, and what HEAD did at the matching call site:
//
//	InviteAccepted             policy: startGuestPolicySync    key: OfferGuestKeyClaim
//	StatusChanged to disabled  policy: "user deactivated"      key: RevokeGuestKeyForAccount
//	                           (handlers/user.go DeactivateUser did both)
//	EmailChanged               policy: "email changed"         key: none (HEAD's e-mail
//	                           routes never touched the key: it is the account's, not
//	                           the address's)
//	UserDeleted                policy: new (HEAD kept it)      key: new (HEAD kept it)
//	UserMergedAway             policy: new (HEAD kept it)      key: new (HEAD kept it)
//
// The last two rows are known differences from HEAD and both only take access
// away: a deleted or merged-away guest keeps neither a Cloudflare guest-policy
// entry nor a live phone-app key.
//
// The two concerns are independent. Each runs under its own recover (concern):
// core recovers a panic in the callback as a whole, which would skip whatever
// came after the panicking concern, so a panic in the guest-policy half must
// never leave the key alive and the reverse. Both are best-effort, as at HEAD:
// neither fails the account event it follows.
func (s *Server) AccountLifecycle() coreauth.Lifecycle {
	return coreauth.Lifecycle{
		InviteAccepted: func(ctx context.Context, e coreauth.InviteAccepted) {
			concern("guest policy", "invite accepted", func() { s.guestOnInviteAccepted(ctx, e) })
			concern("guest key", "invite accepted", func() {
				s.OfferGuestKeyClaim(GuestKeyOffer{
					UserID:            string(e.UserID),
					InviteID:          e.InviteID,
					BindEmailOnAccept: e.BindEmailOnAccept,
					CreatorHeldAdmin:  e.CreatorHeldAdmin,
				})
			})
		},
		StatusChanged: func(ctx context.Context, e coreauth.StatusChanged) {
			if e.To != coreauth.StatusDisabled {
				// A re-enable or any other move does not end access: HEAD's
				// deactivation hooks ran only on the deactivation.
				return
			}
			concern("guest policy", "user deactivated", func() { s.guestOnStatusChanged(ctx, e) })
			concern("guest key", "user deactivated", func() { s.RevokeGuestKeyForAccount(string(e.UserID), "user deactivated") })
		},
		EmailChanged: func(ctx context.Context, e coreauth.EmailChanged) {
			concern("guest policy", "email changed", func() { s.guestOnEmailChanged(ctx, e) })
		},
		UserDeleted: func(ctx context.Context, e coreauth.UserDeleted) {
			concern("guest policy", "user deleted", func() { s.guestOnUserDeleted(ctx, e) })
			concern("guest key", "user deleted", func() { s.RevokeGuestKeyForAccount(string(e.UserID), "user deleted") })
		},
		UserMergedAway: func(ctx context.Context, e coreauth.UserMergedAway) {
			concern("guest policy", "user merged away", func() { s.guestOnUserMergedAway(ctx, e) })
			concern("guest key", "user merged away", func() { s.RevokeGuestKeyForAccount(string(e.UserID), "user merged away") })
		},
	}
}

// concern runs one books reaction to an account event under its own recover. A
// panic is logged at ERROR with the concern and the event and swallowed, so the
// next concern still runs (core's own recover is one level up and would end the
// whole callback).
func concern(name, event string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			inviteAccessLog.Error("account lifecycle (%s): the %s reaction panicked and was contained, the other reactions still ran: %s",
				event, name, logger.SanitizeLogValue(fmt.Sprint(r)))
		}
	}()
	fn()
}
