// file: internal/server/auth_accept_invite.go
// version: 1.3.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef0123456789
// last-edited: 2026-06-09

// Rescued from user_handlers.go during Phase 2 handler extraction.
// handleAcceptInvite remains a *Server method because it bridges auth
// (session creation) and user management in a single transaction.

package server

import (
	"io"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// handleAcceptInvite accepts an invite token and creates a new user session.
// POST /api/v1/auth/accept-invite
func (s *Server) handleAcceptInvite(c *gin.Context) {
	var req struct {
		Token    string `json:"token" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		// Map EOF/empty-body errors to a clear message.
		// HTTP/2 streaming bodies and empty POSTs produce io.EOF or
		// io.ErrUnexpectedEOF. Never echo the raw "EOF" string to the client.
		errMsg := err.Error()
		if err == io.EOF || err == io.ErrUnexpectedEOF || strings.Contains(errMsg, "EOF") {
			httputil.RespondWithBadRequest(c, "request body required: token, password")
			return
		}
		httputil.RespondWithBadRequest(c, errMsg)
		return
	}
	if len(req.Password) < 8 {
		httputil.RespondWithBadRequest(c, "password must be at least 8 characters")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		httputil.InternalError(c, "hash password", err)
		return
	}

	user, err := s.Store().ConsumeInvite(req.Token, "bcrypt", string(hash))
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	sess, err := s.Store().CreateSession(user.ID, c.ClientIP(), c.Request.UserAgent(), 30*24*time.Hour)
	if err != nil {
		httputil.InternalError(c, "create session", err)
		return
	}

	handlers.SetSessionCookie(c, sess.ID, sess.ExpiresAt)
	// Session token is delivered via the HttpOnly cookie only — never in the
	// JSON body (would expose the bearer token to page JS, defeating HttpOnly).
	// Return the safe user shape, not the raw *database.User, which would leak
	// the bcrypt password hash (pen-test finding CRIT-2).
	httputil.RespondWithCreated(c, gin.H{"user": handlers.BuildAuthUserResponse(user), "expires_at": sess.ExpiresAt})
}
