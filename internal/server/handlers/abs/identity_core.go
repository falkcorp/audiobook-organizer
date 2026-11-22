// file: internal/server/handlers/abs/identity_core.go
// version: 0.1.0
// guid: 3b8d5a71-c9e2-4f60-a147-8d2e6b0c9f35
// last-edited: 2026-11-21

package abs

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	coreaudit "github.com/falkcorp/mediacore/audit"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/gin-gonic/gin"
)

// Password login and refresh under core identity (F4-08 B10b1fu).
//
// Under LegacyAuthGin Login and Refresh read the account (password hash, epoch,
// status) from books' store. Once the identity cutover has run that record is
// core's: books' store holds no user rows after M3, and before M3 an epoch bump
// or a disable lands in core only, so a read of books' row would judge the
// account from the wrong source (and, after M3, refuse every phone-app login
// and refresh). A request bound to a legacyauth.Identity (Identity.Bind, set
// only by a server built with server.WithIdentity) therefore sends the account
// question to core's compat handle. What stays the surface's own is unchanged:
// the abs_sess record, the refresh rotation, the access token, the failure
// throttle and the audit trail.

// loginThroughCore is Login's password check under core identity: from the
// reading of the account on, in place of GetUserByUsername, VerifyPassword and
// isActiveUser. Core's Compat.VerifyPassword applies the login ceilings, the
// hashing semaphore, equal timing for an unknown name, status and sign-in
// restriction, and the transparent rehash, so none of those is repeated here
// (SpendVerification and rehashPassword would be a second dummy hash and a write
// to books' user row). The session is stamped with the epoch of the record core
// verified the password against, never a fresh read.
//
// The refusals answer HEAD's status and body for the same condition. Core does
// not tell an unknown name from a wrong password (ErrInvalidCredentials), so
// both are HEAD's 401 "invalid credentials" charged to the failure throttle.
// A correct password for an account that may not sign in is 403 and not
// charged. A source core throttles is 429 with Retry-After, not charged. Every
// other failure (the store down, the hashing semaphore busy) is a 5xx, never a
// 401: the credential is not known to be dead.
func (h *Handler) loginThroughCore(c *gin.Context, id *legacyauth.Identity, username, password, ip, accountKey, throttleKey string, deviceInfo json.RawMessage, now time.Time) {
	u, err := id.Compat.VerifyPassword(c.Request.Context(), username, password, id.Client(c.Request))
	record := func(outcome coreaudit.Outcome, userID, reason string) {
		absauth.AttemptLog.Record(coreaudit.AuthAttempt{
			Action: "login", Outcome: outcome, Mode: servermiddleware.ABSModeJWT,
			SourceIP: ip, Username: username, UserID: userID, Reason: reason,
			Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		})
	}
	switch {
	case err == nil && u != nil:
		// Fall through to the session below.
	case err == nil:
		record(coreaudit.Error, "", "core-no-user")
		respondError(c, http.StatusInternalServerError, "could not verify credentials")
		return
	case errors.Is(err, coreauth.ErrInvalidCredentials):
		// Not charged to h.throttle: core charged this failure to its own
		// per-client and per-account budgets inside VerifyPassword (and delayed
		// it), so a second charge here would count one wrong password twice and
		// lock a source out at half the budget HEAD gave it. The surface's own
		// budget stays for the failures core never sees (an empty credential, a
		// dead refresh token).
		h.auditLoginFailure(c, ip, username, "", "invalid-credentials")
		respondError(c, http.StatusUnauthorized, "invalid credentials")
		return
	case errors.Is(err, coreauth.ErrInactiveUser):
		record(coreaudit.Denied, "", "user-inactive")
		respondError(c, http.StatusForbidden, "account is not active")
		return
	case errors.Is(err, coreauth.ErrResetRequired):
		record(coreaudit.Denied, "", "reset-required")
		respondError(c, http.StatusForbidden, "a password reset is required before this account can sign in")
		return
	case errors.Is(err, coreauth.ErrThrottled):
		record(coreaudit.Throttled, "", "core-throttled")
		absauth.SetRetryAfter(c)
		respondError(c, http.StatusTooManyRequests, "too many failed login attempts — try again later")
		return
	case errors.Is(err, coreauth.ErrBusy):
		record(coreaudit.Error, "", "core-busy")
		absauth.SetRetryAfter(c)
		respondError(c, http.StatusServiceUnavailable, "could not verify credentials")
		return
	default:
		record(coreaudit.Error, "", "user-lookup-error")
		respondError(c, http.StatusInternalServerError, "could not verify credentials")
		return
	}

	h.throttle.Clear(accountKey, throttleKey)
	h.issueSession(c, legacyauth.BooksUserFromCore(u), deviceInfo, servermiddleware.ABSModeJWT, "", now)
}

// refreshUserThroughCore is refreshUser under core identity: the session's user,
// epoch and status are core's. Compat.PrincipalFor is asked with the epoch the
// SESSION was stamped at (never a fresh read, which would match by
// construction); it refuses a user that is not active, whose epoch differs, or
// that core restricts. Called before any session write, as refreshUser is.
//
// Differences from HEAD, both refusals: core judges standing before epoch (a
// session from before a deactivation is 403 here, 401 credentials-changed at
// HEAD), and core answers a user it cannot find with ErrInactiveUser (403 where
// HEAD answered 401 session-user-missing).
func (h *Handler) refreshUserThroughCore(c *gin.Context, id *legacyauth.Identity, ip string, session *database.ABSSession, mode string) (*database.User, bool) {
	p, err := id.Compat.PrincipalFor(c.Request.Context(), &coreauth.User{ID: coreauth.UserID(session.UserID)}, nil, session.CredentialEpoch, id.Client(c.Request))
	deny := func(outcome coreaudit.Outcome, reason string) {
		absauth.AttemptLog.Record(coreaudit.AuthAttempt{
			Action: "refresh", Outcome: outcome, Mode: mode, SourceIP: ip,
			UserID: session.UserID, SessionID: session.ID, Reason: reason,
			Path: c.Request.URL.Path,
		})
	}
	switch {
	case err == nil && p != nil && p.User != nil:
		return legacyauth.BooksUserFromCore(p.User), true
	case err == nil:
		h.errorRefresh(c, ip, "core-no-user", "could not load user")
	case errors.Is(err, coreauth.ErrStaleEpoch):
		h.failRefresh(c, ip, session.ID, "credentials-changed")
	case errors.Is(err, coreauth.ErrInvalidCredentials):
		h.failRefresh(c, ip, session.ID, "session-user-missing")
	case errors.Is(err, coreauth.ErrInactiveUser):
		deny(coreaudit.Denied, "user-inactive")
		respondError(c, http.StatusForbidden, "account is not active")
	case errors.Is(err, coreauth.ErrResetRequired):
		deny(coreaudit.Denied, "reset-required")
		respondError(c, http.StatusForbidden, "account is not active")
	case errors.Is(err, coreauth.ErrThrottled):
		deny(coreaudit.Throttled, "core-throttled")
		absauth.SetRetryAfter(c)
		respondError(c, http.StatusTooManyRequests, "too many failed refresh attempts from this source — try again later")
	default:
		// The store is down or the answer is unclassified: transient, so 5xx
		// and the client keeps its session (§1.7.3 item 3).
		h.errorRefresh(c, ip, "user-lookup-error", "could not load user")
	}
	return nil, false
}
