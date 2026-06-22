// file: internal/server/auth_temp_login.go
// version: 1.2.0
// guid: 5b6c7d8e-9f0a-1b2c-3d4e-5f6a7b8c9d0e
// last-edited: 2026-06-22

// Temp-login token: admin mints a short-lived single-use URL for a user.
// User clicks the URL → server consumes the token → 24h session cookie
// set. Lets the admin sign themselves (or someone else) in on a new
// device without re-entering credentials. No SMTP plumbing needed —
// admin delivers the URL out-of-band.

package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// tempLoginEntry tracks one minted token. Tokens are kept in memory
// only — a server restart invalidates everything, which is fine given
// the 15-minute TTL.
type tempLoginEntry struct {
	UserID    string
	ExpiresAt time.Time
}

var (
	tempLoginMu      sync.Mutex
	tempLoginTokens  = map[string]tempLoginEntry{}
	tempLoginPurgeAt time.Time
)

// newTempLoginToken returns 32 hex chars (16 random bytes). Cryptographically
// strong; URL-safe; not guessable.
func newTempLoginToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// purgeExpiredTempTokens drops expired entries. Called opportunistically
// (no background goroutine — map stays small at typical admin volume).
func purgeExpiredTempTokens() {
	if time.Since(tempLoginPurgeAt) < 5*time.Minute {
		return
	}
	now := time.Now()
	for tok, entry := range tempLoginTokens {
		if entry.ExpiresAt.Before(now) {
			delete(tempLoginTokens, tok)
		}
	}
	tempLoginPurgeAt = now
}

// createTempLoginToken handles POST /api/v1/auth/temp-tokens.
// Admin-only. Body: {user_id: "..."} — mints a token + URL the target
// user can click to log in. Token is single-use, 15-min TTL, exchanges
// for a 24h session.
func (s *Server) createTempLoginToken(c *gin.Context) {
	if s.Store() == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		httputil.RespondWithBadRequest(c, "user_id is required")
		return
	}

	user, err := s.Store().GetUserByID(req.UserID)
	if err != nil || user == nil {
		httputil.RespondWithNotFound(c, "user", req.UserID)
		return
	}

	token, err := newTempLoginToken()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to generate token")
		return
	}
	expires := time.Now().Add(handlers.TempLoginTokenTTL)

	tempLoginMu.Lock()
	purgeExpiredTempTokens()
	tempLoginTokens[token] = tempLoginEntry{UserID: user.ID, ExpiresAt: expires}
	tempLoginMu.Unlock()

	// Build the login URL. If ExternalURL is configured, use it as the
	// canonical origin — never trust the inbound Host header, which can be
	// spoofed to redirect the token to an attacker-controlled origin.
	// When ExternalURL is empty, return only the relative path so the admin
	// can prepend the known server address out-of-band.
	relativePath := fmt.Sprintf("/auth/temp-login?token=%s", token)
	loginURL := relativePath
	if s.externalURL != "" {
		loginURL = s.externalURL + relativePath
	}

	httputil.RespondWithCreated(c, gin.H{
		"token":      token,
		"login_url":  loginURL,
		"expires_at": expires,
		"user": gin.H{
			"id":       user.ID,
			"username": user.Username,
		},
		"session_ttl_hours": int(handlers.DefaultSessionTTL.Hours()),
	})
}

// consumeTempLoginToken handles GET /auth/temp-login?token=xxx.
// Public — gated only by the token's validity + single-use semantics.
// On success: creates a 24h session, sets the session cookie, and
// redirects to the SPA root. On failure: redirects to /login with an
// error query param so the SPA can show a message.
func (s *Server) consumeTempLoginToken(c *gin.Context) {
	if s.Store() == nil {
		c.Redirect(http.StatusSeeOther, "/login?error=server")
		return
	}
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		c.Redirect(http.StatusSeeOther, "/login?error=missing_token")
		return
	}

	tempLoginMu.Lock()
	entry, ok := tempLoginTokens[token]
	if ok {
		// Single-use: delete on first read so a leaked URL can't be
		// replayed even before the TTL fires.
		delete(tempLoginTokens, token)
	}
	tempLoginMu.Unlock()

	if !ok || time.Now().After(entry.ExpiresAt) {
		c.Redirect(http.StatusSeeOther, "/login?error=invalid_or_expired_token")
		return
	}

	user, err := s.Store().GetUserByID(entry.UserID)
	if err != nil || user == nil {
		c.Redirect(http.StatusSeeOther, "/login?error=user_not_found")
		return
	}
	// Tighten: only "active" users can consume a temp-login token.
	// Catches the case where an admin minted a token for an account
	// that was later disabled / suspended — without this check the
	// user could still log in via the stale URL.
	if !strings.EqualFold(user.Status, "active") {
		c.Redirect(http.StatusSeeOther, "/login?error=account_inactive")
		return
	}

	session, err := s.Store().CreateSession(
		user.ID,
		strings.TrimSpace(c.ClientIP()),
		strings.TrimSpace(c.Request.UserAgent()),
		handlers.DefaultSessionTTL,
	)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/login?error=session_failed")
		return
	}
	handlers.SetSessionCookie(c, session.ID, session.ExpiresAt)
	c.Redirect(http.StatusSeeOther, "/")
}

// permTempLoginMint returns the permission required to mint temp-login
// tokens. Kept here so the route registration stays small.
func permTempLoginMint() auth.Permission { return auth.PermUsersManage }
