// file: internal/server/auth_reset_password.go
// version: 1.6.1
// guid: 6b2d94f1-7a08-4c53-9e16-3d8f05a2c761
// last-edited: 2026-10-30

package server

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// handleResetPassword handles POST /api/v1/users/:id/reset-password.
//
// It hands the admin a single-use link the target user clicks to get a session,
// after which they set their own password via PUT /api/v1/auth/me/password. The
// admin never chooses or sees the new password.
//
// # Why this is a *Server method rather than a UserHandler one
//
// The temp-login token store is package state in this package, and `server`
// imports `handlers`, so a handlers-side implementation cannot reach it without
// an import cycle. Same reasoning as handleAcceptInvite.
//
// # What this replaces, and why the old version could never have worked
//
// The previous implementation minted a database.Invite for the existing user's
// username. That was broken twice over:
//
//  1. It passed RoleID: "" and CreateInvite rejects an empty role_id, so every
//     call returned 500 — for every user, 100% of the time.
//  2. Fixing only that would have been WORSE. Invites are the new-account
//     mechanism: ConsumeInvite creates a user and explicitly refuses a token
//     whose username already exists ("username %q taken since invite was
//     created"). So a role-carrying invite for an existing user would have
//     produced a token that hands the admin a reset link which then fails at
//     redemption — trading a loud 500 for a silent dead end.
//
// The 500 was the only thing preventing a broken reset link from reaching a real
// user, so this is a behaviour fix, not merely an error-handling one.
//
// Note there is a second, already-working path for the different intent of "an
// admin sets a password directly": PUT /api/v1/auth/me/password accepts a
// user_id and skips the current-password check for callers holding
// users.manage. This endpoint deliberately does NOT duplicate that — it exists
// for the case where the user should choose their own password.
func (s *Server) handleResetPassword(c *gin.Context) {
	if s.Ops() == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	id := c.Param("id")
	user, err := s.Ops().GetUserByID(id)
	if err != nil || user == nil {
		httputil.RespondWithNotFound(c, "user", id)
		return
	}

	// What this does: it signs the user out of every ABS (mobile) client and
	// every web-UI session, and hands the admin a one-time login link so the user
	// can get in and choose a new password. What it does NOT do: the old password
	// keeps working until the user sets a new one, so this is not
	// compromised-account handling (filed in todo.d as an owner decision).
	//
	// Signing the sessions out is the CredentialEpoch bump (every ABS refresh and
	// request, and every web-session request, checks it). ABS sessions slide with
	// use and have no absolute cap by default, and a web session lasts its TTL,
	// so this is what ends them. Fail closed: no link unless that write landed.
	// BumpUserCredentialEpoch changes only the epoch, on the fresh row, so it
	// cannot revert a status or password change that landed after our read. The
	// revokes after it are cleanup. The link, redeemed later, mints a session
	// stamped with the epoch current at that moment, so it is valid.
	creds := handlers.ResolveUserCredentialWriter(s.Ops())
	if creds == nil {
		httputil.RespondWithInternalError(c, "password resets are not supported by this store; no reset link was issued")
		return
	}
	bumped, err := creds.BumpUserCredentialEpoch(user.ID)
	if err != nil {
		if errors.Is(err, database.ErrUserNotFound) {
			httputil.RespondWithNotFound(c, "user", id)
			return
		}
		httputil.RespondWithInternalError(c, "could not sign the user out of their sessions; no reset link was issued, try again")
		return
	}
	handlers.RevokeAllABSSessions(handlers.ResolveABSSessionRevoker(s.Ops()), user.ID, "password-reset-link")
	handlers.RevokeStaleWebSessions(s.Ops(), user.ID, bumped.CredentialEpoch, "password-reset-link")

	// Mint from the bumped row: the link carries the epoch the reset produced, so
	// it is redeemable, while any later credential change kills it.
	payload, ok := s.mintTempLoginPayload(c, bumped)
	if !ok {
		return
	}
	// 200, not 201: the previous implementation answered 200 and the Users page
	// reads the body without checking the status. Nothing is created in the
	// database here either — the token lives in memory until it is used.
	httputil.RespondWithOK(c, payload)
}
