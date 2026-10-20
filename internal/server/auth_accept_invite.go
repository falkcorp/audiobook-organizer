// file: internal/server/auth_accept_invite.go
// version: 1.10.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef0123456789
// last-edited: 2026-10-20

// Rescued from user_handlers.go during Phase 2 handler extraction.
// handleAcceptInvite remains a *Server method because it bridges auth
// (session creation) and user management in a single transaction.

package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
)

// acceptInviteInvalidMessage is the one answer to every refused token.
const acceptInviteInvalidMessage = "this invitation link is not valid; ask the person who invited you for a new one"

// isInviteRefusal reports whether ConsumeInvite refused the invite itself
// (a client-side problem) rather than failing on storage.
//
// ErrInviteEmailTaken (another account holds the invited address verified)
// is one of them: a distinct answer would tell whoever holds the link that
// the address has a verified account somewhere.
func isInviteRefusal(err error) bool {
	return errors.Is(err, database.ErrInviteEmailTaken) ||
		errors.Is(err, database.ErrInviteNotFound) ||
		errors.Is(err, database.ErrInviteUsed) ||
		errors.Is(err, database.ErrInviteExpired) ||
		errors.Is(err, database.ErrInviteUsernameTaken)
}

// handleAcceptInvite accepts an invite token and creates a new user session.
// POST /api/v1/auth/accept-invite
//
// The invite is consumed exactly once (ConsumeInvite runs under the store's
// invite lock). The invite's email is bound to the new user, verified and
// granted SSO only when the invitation was emailed to that address (holding
// the token then proves the inbox) by an active admin; all of it is decided
// in the same store batch.
// For a granted email, the address is then added to the Cloudflare Access
// guest policy in the background when that sync is configured. Every outcome
// is recorded on the invite for the admin and never affects this request.
// Accounts that had only typed the address into their profile lose it to the
// accept, as to an IdP-verified login, with an audit entry.
func (s *Server) handleAcceptInvite(c *gin.Context) {
	// The rate limiter on this route parsed the body and checked its token;
	// readAcceptInviteBody returns that same parse, so the token consumed
	// here is exactly the one the limiter counted.
	p := readAcceptInviteBody(c)
	switch {
	case p.tooLarge:
		httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, "request body too large", "PAYLOAD_TOO_LARGE")
		return
	case p.empty:
		httputil.RespondWithBadRequest(c, "request body required: token, password")
		return
	case !p.parsed:
		httputil.RespondWithBadRequest(c, "request body must be a JSON object with token and password")
		return
	case p.req.Token == "" || p.req.Password == "":
		httputil.RespondWithBadRequest(c, "request body required: token, password")
		return
	}
	req := p.req
	if len(req.Password) < 8 {
		httputil.RespondWithBadRequest(c, "password must be at least 8 characters")
		return
	}

	algo, hash, err := absauth.HashPassword(req.Password)
	if err != nil {
		httputil.InternalError(c, "hash password", err)
		return
	}

	user, err := s.Ops().ConsumeInvite(req.Token, algo, hash)
	if err != nil {
		if isInviteRefusal(err) {
			// Missing, used, expired and similar refusals share one answer:
			// the route is public, so distinct messages would tell a caller
			// which tokens exist. The detail stays here, never the token.
			inviteAccessLog.Warn("invite accept refused: %s (client %s)",
				logger.SanitizeLogValue(err.Error()), logger.SanitizeLogValue(c.ClientIP()))
			httputil.RespondWithBadRequest(c, acceptInviteInvalidMessage)
			return
		}
		httputil.InternalError(c, "consume invite", err)
		return
	}
	s.recordAcceptDisplacement(req.Token, user.ID)
	s.startGuestPolicySync(req.Token, user.ID)

	sess, err := s.Ops().CreateSession(user.ID, c.ClientIP(), c.Request.UserAgent(), 30*24*time.Hour)
	if err != nil {
		httputil.InternalError(c, "create session", err)
		return
	}

	handlers.SetSessionCookie(c, sess.ID, sess.ExpiresAt)
	// Session token is delivered via the HttpOnly cookie only — never in the
	// JSON body (would expose the bearer token to page JS, defeating HttpOnly).
	// Return the safe user shape, not the raw *database.User, which would leak
	// the password hash (pen-test finding CRIT-2).
	httputil.RespondWithCreated(c, gin.H{"user": handlers.BuildAuthUserResponse(user), "expires_at": sess.ExpiresAt})
}

// recordAcceptDisplacement writes the audit entry and a warning when the
// accept took its address from accounts that had only typed it in (the store
// records them on the invite, in the accept's own batch).
func (s *Server) recordAcceptDisplacement(token, newUserID string) {
	inv, err := s.storeForWiring().GetInvite(token)
	if err != nil || inv == nil || len(inv.DisplacedUserIDs) == 0 {
		return
	}
	inviteAccessLog.Warn("invite accept: an emailed invitation took its address back from unverified account(s) %v; new account %s",
		inv.DisplacedUserIDs, newUserID)
	s.recordEmailDisplaced(newUserID, inv.DisplacedUserIDs, emailDisplacedByInvite)
}
