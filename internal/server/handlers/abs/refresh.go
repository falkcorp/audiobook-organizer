// file: internal/server/handlers/abs/refresh.go
// version: 1.5.0
// guid: a95c6f04-8e21-4d7b-9350-6b1c8d2fe7a3
// last-edited: 2026-10-22

package abs

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Refresh handles POST /auth/refresh.
//
// THE STATUS CODES ARE THE HARD PART. §1.7.3 item 3: 401/403 from this endpoint is
// the one signal that forces a client logout, so it must be returned ONLY for a
// genuinely dead refresh token. A store error, a timeout, or any other transient
// failure must be 5xx — a 401 on a transient error logs the user out and, for
// AudioBooth, loses the in-flight request with no retry-and-refresh interceptor.
//
// Two credential paths:
//
//  1. A verified Cf-Access-Jwt-Assertion → ALWAYS succeeds (§3.0.1). Identity arrives
//     with every request from Cloudflare, so nothing depends on the client having
//     correctly persisted a rotated token; ABS's "Session not found" logout class —
//     its single most common real-world complaint — cannot occur in this mode.
//  2. Otherwise the rotation-plus-grace logic of §3.4, under a per-session
//     single-flight lock.
//
// SLIDING EXPIRY. Every successful branch — rotate, grace replay and the CF reuse —
// moves the session's ExpiresAt to cfg.SessionExpiry(now), i.e. now plus the idle
// TTL, capped by the optional absolute lifetime. Until 2026-10-19 no branch did, so
// ExpiresAt stayed where login put it and every session died 30 days after login no
// matter how often it was refreshed.
func (h *Handler) Refresh(c *gin.Context) {
	ip := strings.TrimSpace(c.ClientIP())
	now := h.now()
	presented := refreshTokenFromRequest(c)

	// ── Path 1: CF-backed identity — never "Session not found" ───────────────
	if identity, authErr := h.resolver.ResolveCFAssertion(c); authErr != nil {
		servermiddleware.AbortABSAuth(c, "refresh", authErr)
		return
	} else if identity != nil {
		h.refreshForCFIdentity(c, identity, presented, now)
		return
	}

	// ── Path 2: our own refresh token ────────────────────────────────────────
	if !h.cfg.Modes.JWT {
		servermiddleware.AbortABSAuth(c, "refresh", &servermiddleware.ABSAuthError{
			Status: http.StatusUnauthorized, Reason: "token-refresh-disabled",
			Message: "token refresh is disabled on this server",
		})
		return
	}
	if h.throttle.IPBlocked(absauth.RefreshKey(h.throttleKey(c))) {
		// 429, not 401: a rate-limit answer must not be mistaken for a dead token.
		absauth.Audit(absauth.AuditEvent{
			Action: "refresh", Outcome: absauth.OutcomeThrottled, SourceIP: ip,
			Reason: "ip-failure-budget-exhausted", Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		})
		respondError(c, http.StatusTooManyRequests, "too many failed refresh attempts from this source — try again later")
		return
	}
	if presented == "" {
		h.failRefresh(c, ip, "", "refresh-token-missing")
		return
	}

	hash := absauth.HashRefreshToken(presented)
	session, err := h.store.GetABSSessionByRefreshHash(hash)
	if err != nil {
		h.errorRefresh(c, ip, "refresh-lookup-error", "could not verify session")
		return
	}
	if session == nil {
		// Genuinely dead: not current, not previous, or never existed.
		h.failRefresh(c, ip, "", "refresh-token-unknown")
		return
	}

	// §3.4 step 1: serialize concurrent refreshes of the SAME session. Different
	// sessions (i.e. different devices) never contend.
	lock := h.lockForSession(session.ID)
	lock.Lock()
	defer lock.Unlock()

	// Re-read under the lock: another request may have rotated while we waited, in
	// which case our token is now the PREVIOUS one and we must take the grace path.
	session, err = h.store.GetABSSession(session.ID)
	if err != nil {
		h.errorRefresh(c, ip, "session-reload-error", "could not verify session")
		return
	}
	if session == nil {
		h.failRefresh(c, ip, "", "session-vanished")
		return
	}
	if !session.Live(now) {
		h.failRefresh(c, ip, session.ID, "session-not-live")
		return
	}
	if h.cfg.SessionPastMaxLifetime(session.CreatedAt, now) {
		h.failRefresh(c, ip, session.ID, "session-max-lifetime")
		return
	}
	// The user is checked BEFORE anything is written. A rotate or grace touch
	// slides ExpiresAt, so doing it first and refusing afterwards would let a
	// locked or deleted user's session keep extending itself on every attempt.
	user, ok := h.refreshUser(c, ip, session, servermiddleware.ABSModeJWT)
	if !ok {
		return
	}

	var refreshToken string
	switch {
	case absauth.ConstantTimeEqualHash(hash, session.RefreshTokenHash):
		// §3.4 step 2 — ROTATE. The current token becomes the previous one and stays
		// acceptable for the grace window, which bounds replay of a stolen old token
		// to a single window while making a lost round-trip recoverable.
		session.PrevRefreshTokenHash = session.RefreshTokenHash
		session.Generation++
		refreshToken = h.cfg.DeriveRefreshToken(session.ID, session.Seed, session.Generation)
		session.RefreshTokenHash = absauth.HashRefreshToken(refreshToken)
		session.GraceUntil = now.Add(h.cfg.RefreshGrace)
		session.LastUsedAt = now
		session.ExpiresAt = h.cfg.SessionExpiry(session.CreatedAt, now)
		if err := h.store.UpdateABSSession(session); err != nil {
			if errors.Is(err, database.ErrABSSessionRevoked) {
				// A logout landed after our read: the session really is dead.
				h.failRefresh(c, ip, session.ID, "session-revoked")
				return
			}
			// ErrABSSessionStale lands here too: another writer rotated since our
			// read. 5xx so the client retries and takes the grace path.
			h.errorRefresh(c, ip, "session-update-error", "could not rotate session")
			return
		}

	case absauth.ConstantTimeEqualHash(hash, session.PrevRefreshTokenHash) && session.InGrace(now):
		// §3.4 step 3 — IDEMPOTENT REPLAY. This device's previous refresh never
		// reached it (concurrent request, dropped response, retried write). Hand back
		// the ALREADY-MINTED current pair without rotating again: rotating here is
		// what orphans one of two simultaneous refreshes and produces the "Session
		// not found" logout the grace window exists to prevent.
		//
		// The token is re-derived from (id, seed, generation) keyed by the server
		// secret, so no live credential is ever stored in the database.
		refreshToken = h.cfg.DeriveRefreshToken(session.ID, session.Seed, session.Generation)
		session.LastUsedAt = now
		session.ExpiresAt = h.cfg.SessionExpiry(session.CreatedAt, now)
		if err := h.store.UpdateABSSession(session); err != nil {
			if errors.Is(err, database.ErrABSSessionRevoked) {
				h.failRefresh(c, ip, session.ID, "session-revoked")
				return
			}
			// The rotate that minted this pair already slid ExpiresAt to within the
			// grace window of now, so a failed touch costs at most RefreshGrace of
			// session life. Do not fail the refresh over it.
			absauth.Audit(absauth.AuditEvent{
				Action: "refresh", Outcome: absauth.OutcomeError, Mode: servermiddleware.ABSModeJWT,
				SourceIP: ip, SessionID: session.ID, Reason: "last-used-touch-failed",
			})
		}

	default:
		// Beyond the grace window, or a token from a retired generation.
		h.failRefresh(c, ip, session.ID, "refresh-token-retired")
		return
	}

	h.completeRefresh(c, user, session, refreshToken, servermiddleware.ABSModeJWT, "", ip, now)
}

// refreshForCFIdentity implements the "always succeeds" branch.
//
// It reuses the session the presented refresh token points at when that session
// belongs to this same user, so a Mode C client keeps one session per device instead
// of accumulating one per refresh. Otherwise it mints a fresh session. Either way the
// answer is 200 — with a CF-backed identity there is no such thing as a dead session.
func (h *Handler) refreshForCFIdentity(c *gin.Context, identity *servermiddleware.ABSIdentity, presented string, now time.Time) {
	ip := strings.TrimSpace(c.ClientIP())
	user := identity.User

	if presented != "" {
		if session, refreshToken, ok := h.rotateForCF(absauth.HashRefreshToken(presented), user, now); ok {
			h.completeRefresh(c, user, session, refreshToken, servermiddleware.ABSModeCF, identity.Email, ip, now)
			return
		}
		// Otherwise mint a new session: in Mode C we must not fail.
	}

	session, refreshToken, err := h.createSession(user, deviceInfoFromRequest(c), ip, c.Request.UserAgent(), now)
	if err != nil {
		// The only failure Mode C can produce is a genuine server error, and it is
		// reported as 5xx so the client retries rather than logging out.
		h.errorRefresh(c, ip, "session-create-failed", "could not create session")
		return
	}
	h.completeRefresh(c, user, session, refreshToken, servermiddleware.ABSModeCF, identity.Email, ip, now)
}

// rotateForCF rotates the session the presented refresh token belongs to, when it
// is this user's, still live and issued under the user's current credentials, and
// reports false otherwise (or on any store error) so the caller mints a fresh
// session, stamped with the current epoch, instead.
//
// The lookup by hash runs unlocked, so the row is re-read and every condition
// re-checked under the per-session lock before rotating. Rotating the unlocked
// copy could write an older generation back over a rotation that landed in
// between and orphan the token that device was just given; the store refuses that
// write too (ErrABSSessionStale), but the re-read is what keeps it from happening.
func (h *Handler) rotateForCF(hash string, user *database.User, now time.Time) (*database.ABSSession, string, bool) {
	userID := user.ID
	found, err := h.store.GetABSSessionByRefreshHash(hash)
	if err != nil || found == nil || found.UserID != userID {
		return nil, "", false
	}
	lock := h.lockForSession(found.ID)
	lock.Lock()
	defer lock.Unlock()
	session, err := h.store.GetABSSession(found.ID)
	if err != nil || session == nil || session.UserID != userID || !session.Live(now) ||
		!session.CredentialsCurrent(user) ||
		h.cfg.SessionPastMaxLifetime(session.CreatedAt, now) ||
		(hash != session.RefreshTokenHash && hash != session.PrevRefreshTokenHash) {
		return nil, "", false
	}
	session.PrevRefreshTokenHash = session.RefreshTokenHash
	session.Generation++
	refreshToken := h.cfg.DeriveRefreshToken(session.ID, session.Seed, session.Generation)
	session.RefreshTokenHash = absauth.HashRefreshToken(refreshToken)
	session.GraceUntil = now.Add(h.cfg.RefreshGrace)
	session.LastUsedAt = now
	session.ExpiresAt = h.cfg.SessionExpiry(session.CreatedAt, now)
	if err := h.store.UpdateABSSession(session); err != nil {
		return nil, "", false
	}
	return session, refreshToken, true
}

// refreshUser loads the session's user and refuses the refresh, writing the
// response, when it is gone (401: the session really is dead), inactive (403),
// unreadable (5xx), or when the session was issued under credentials that have
// since changed (401, credentials-changed: a password change, reset or
// deactivation bumped User.CredentialEpoch). Called before any session write.
//
// The epoch is checked, never re-stamped: a refresh carries the session's stamp
// forward, so a session minted with an old password cannot be refreshed into one
// that passes.
func (h *Handler) refreshUser(c *gin.Context, ip string, session *database.ABSSession, mode string) (*database.User, bool) {
	sessionID := session.ID
	user, err := h.store.GetUserByID(session.UserID)
	if err != nil {
		h.errorRefresh(c, ip, "user-lookup-error", "could not load user")
		return nil, false
	}
	if user == nil {
		h.failRefresh(c, ip, sessionID, "session-user-missing")
		return nil, false
	}
	// Before the active check: a session from before a deactivation is dead
	// (401, sign in again), not merely suspended (403).
	if !session.CredentialsCurrent(user) {
		h.failRefresh(c, ip, sessionID, "credentials-changed")
		return nil, false
	}
	if !isActiveUser(user) {
		absauth.Audit(absauth.AuditEvent{
			Action: "refresh", Outcome: absauth.OutcomeDenied, Mode: mode, SourceIP: ip,
			UserID: user.ID, Username: user.Username, SessionID: sessionID,
			Reason: "user-inactive", Path: c.Request.URL.Path,
		})
		respondError(c, http.StatusForbidden, "account is not active")
		return nil, false
	}
	return user, true
}

// completeRefresh mints the access token and writes the shared auth response for
// a user the caller has already loaded and checked: refreshUser on the JWT path,
// ResolveCFAssertion (which refuses inactive users) on the CF path.
func (h *Handler) completeRefresh(c *gin.Context, user *database.User, session *database.ABSSession, refreshToken, mode, email, ip string, now time.Time) {
	// §1.8.8 item 5: accessToken must be a real, parseable JWT with a numeric exp.
	accessToken, _, err := h.cfg.MintAccessToken(user.ID, session.ID, now)
	if err != nil {
		h.errorRefresh(c, ip, "mint-access-failed", "could not issue token")
		return
	}

	progress, bookmarks, err := h.userPayload(user.ID)
	if err != nil {
		h.errorRefresh(c, ip, "user-data-unavailable", "could not load user data")
		return
	}

	absauth.Audit(absauth.AuditEvent{
		Action: "refresh", Outcome: absauth.OutcomeSuccess, Mode: mode, SourceIP: ip,
		UserID: user.ID, Username: user.Username, Email: email, SessionID: session.ID,
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})
	// No throttle refund here: a refresh is not a password login, it must never
	// give back source budget (Clear takes back only the same source's failures
	// against the same account, and a refresh names none).

	respondJSON(c, http.StatusOK, h.buildAuthResponse(user, accessToken, refreshToken, progress, bookmarks))
}

// failRefresh answers 401 for a genuinely dead refresh token and charges the source
// IP's failure budget.
func (h *Handler) failRefresh(c *gin.Context, ip, sessionID, reason string) {
	h.throttle.RecordFailure("", absauth.RefreshKey(h.throttleKey(c)))
	absauth.Audit(absauth.AuditEvent{
		Action: "refresh", Outcome: absauth.OutcomeFailure, Mode: servermiddleware.ABSModeJWT,
		SourceIP: ip, SessionID: sessionID, Reason: reason,
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})
	// ABS's own wording, which clients log verbatim.
	respondError(c, http.StatusUnauthorized, "Session not found")
}

// errorRefresh answers 5xx for a transient failure. It deliberately does NOT charge
// the failure budget and does NOT return 401: §1.7.3 item 3 — the session must survive.
func (h *Handler) errorRefresh(c *gin.Context, ip, reason, message string) {
	absauth.Audit(absauth.AuditEvent{
		Action: "refresh", Outcome: absauth.OutcomeError, SourceIP: ip, Reason: reason,
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})
	respondError(c, http.StatusInternalServerError, message)
}

// refreshTokenFromRequest reads the refresh token.
//
// The `x-refresh-token` header is the primary form: that is what AudioBooth sends
// (§1.8.8 item 5). The JSON body and a cookie are accepted as a superset so a client
// that differs still works — where the two audited clients disagree, implement the
// superset. The Authorization header is deliberately NOT consulted: it carries the
// ACCESS token, and treating one as the other would blur the two credentials.
func refreshTokenFromRequest(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if v := strings.TrimSpace(c.GetHeader("x-refresh-token")); v != "" {
		return v
	}
	if body := readJSONBody(c); body != nil {
		for _, key := range []string{"refreshToken", "refresh_token"} {
			if v, ok := body[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	if ck, err := c.Cookie("refresh_token"); err == nil && strings.TrimSpace(ck) != "" {
		return strings.TrimSpace(ck)
	}
	return ""
}

// deviceInfoFromRequest pulls an optional deviceInfo object out of the request body.
func deviceInfoFromRequest(c *gin.Context) json.RawMessage {
	body := readJSONBody(c)
	if body == nil {
		return nil
	}
	raw, ok := body["deviceInfo"]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	return encoded
}

// readJSONBody decodes and CACHES the request body so several helpers can inspect it
// without racing to consume the single-use reader. A missing or malformed body yields
// nil rather than an error: on /auth/refresh the body is optional.
func readJSONBody(c *gin.Context) map[string]any {
	const key = "abs_json_body"
	if cached, ok := c.Get(key); ok {
		body, _ := cached.(map[string]any)
		return body
	}
	var body map[string]any
	if c.Request != nil && c.Request.Body != nil {
		if raw, err := io.ReadAll(c.Request.Body); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
	}
	c.Set(key, body)
	return body
}
