// file: internal/server/auth_accept_invite.go
// version: 1.7.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef0123456789
// last-edited: 2026-10-19

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
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
)

// handleAcceptInvite accepts an invite token and creates a new user session.
// POST /api/v1/auth/accept-invite
//
// The invite is consumed exactly once (ConsumeInvite runs under the store's
// invite lock). The invite's email is bound to the new user only when the
// invitation was emailed to that address (holding the token then proves the
// inbox), and it gets an SSO grant (inviteGrantAllowed) only when the invite's
// creator is an active admin; both decisions are made in the same store batch.
// For a granted email, the address is then added to the Cloudflare Access
// guest policy in the background when that sync is configured. Every outcome
// is recorded on the invite for the admin and never affects this request.
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
		if errors.Is(err, database.ErrInviteEmailTaken) {
			httputil.RespondWithConflict(c, "this invitation's email address already belongs to an account; ask the person who invited you for a new invitation")
			return
		}
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
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
