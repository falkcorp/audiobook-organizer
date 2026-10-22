// file: internal/server/handlers/abs/login.go
// version: 1.7.0
// guid: 8e40b7d2-6c15-4a93-b027-51d8f3ac9e64
// last-edited: 2026-10-22

package abs

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// throttleKey is the bucket the failure throttle charges this request to:
// Options.ThrottleKey's answer, else the peer address (c.ClientIP()).
func (h *Handler) throttleKey(c *gin.Context) string {
	if h.throttleKeyFn != nil {
		if k := strings.TrimSpace(h.throttleKeyFn(c)); k != "" {
			return k
		}
	}
	return strings.TrimSpace(c.ClientIP())
}

// loginRequest is the ABS login body. deviceInfo is optional and echoed back on
// GET /api/me/sessions.
type loginRequest struct {
	Username   string          `json:"username"`
	Password   string          `json:"password"`
	DeviceInfo json.RawMessage `json:"deviceInfo"`
}

// Login handles POST /login.
//
// Two credential paths, one response shape:
//
//  1. A VERIFIED Cf-Access-Jwt-Assertion skips the password check entirely (§3.0.1).
//     The Cloudflare edge has already authenticated a real person against the IdP, so
//     asking for a password again would be theatre — and there is no password to ask
//     for when the user was JIT-provisioned. An assertion that does not verify is a
//     hard 401 and one whose email is not on the allowlist is a hard 403; neither
//     falls through to the password path, so the CF path cannot be probed for free.
//  2. Otherwise a username/password login (Mode B), throttled per source IP.
//
// Both paths return the identical token-shaped body, so no client can tell the modes
// apart — that is what lets one build serve both topologies.
func (h *Handler) Login(c *gin.Context) {
	ip := strings.TrimSpace(c.ClientIP())
	now := h.now()

	// Read the body first, tolerantly. §1.8.8 item 9: these clients send
	// Content-Type: application/json on every request including bodyless ones, and in
	// Mode C there may be no credentials in the body at all.
	req, bodyErr := decodeLoginBody(c)

	// ── Path 1: verified Cloudflare Access assertion ─────────────────────────
	if identity, authErr := h.resolver.ResolveCFAssertion(c); authErr != nil {
		servermiddleware.AbortABSAuth(c, "login", authErr)
		return
	} else if identity != nil {
		h.issueSession(c, identity.User, req.DeviceInfo, servermiddleware.ABSModeCF, identity.Email, now)
		return
	}

	// ── Path 2: username + password ──────────────────────────────────────────
	if !h.cfg.Modes.JWT {
		// Hardened to ABS_AUTH_MODES=cf: password login is closed.
		servermiddleware.AbortABSAuth(c, "login", &servermiddleware.ABSAuthError{
			Status: http.StatusUnauthorized, Reason: "password-login-disabled",
			Message: "password login is disabled on this server",
		})
		return
	}
	if bodyErr != nil {
		absauth.Audit(absauth.AuditEvent{
			Action: "login", Outcome: absauth.OutcomeFailure, SourceIP: ip,
			Reason: "malformed-body", Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		})
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}

	// The hard limit is checked BEFORE any credential work so a spent source cannot
	// keep probing. It is 429, never 401/403 — a 4xx-auth answer would tell a client
	// its credential is dead.
	throttleKey := h.throttleKey(c)
	// Only this door's own budget is read. The refresh endpoint spends a separate
	// one (absauth.RefreshKey), and neither password login looks at it: a client
	// holding a stale token that burns its refresh budget must still be able to
	// sign in with its password, which is how it gets a fresh token.
	if h.throttle.IPBlocked(throttleKey) {
		absauth.Audit(absauth.AuditEvent{
			Action: "login", Outcome: absauth.OutcomeThrottled, SourceIP: ip,
			Username: req.Username, Reason: "ip-failure-budget-exhausted",
			Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		})
		absauth.SetRetryAfter(c)
		respondError(c, http.StatusTooManyRequests, "too many failed login attempts from this source — try again later")
		return
	}

	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" {
		h.throttle.Delay(h.throttle.RecordFailure("", throttleKey))
		h.auditLoginFailure(c, ip, username, "", "missing-credentials")
		respondError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// Everything below is keyed on the normalised SUBMITTED username, never on the
	// user row, so a name that does not exist behaves exactly like one that does.
	accountKey := absauth.AccountKey(username)
	absauth.NoteLoginAccount(c, accountKey) // for the login ceilings' same-account refund
	// While the aggregate login ceilings are spent (absauth.MarkLoginSaturated) the
	// login is delayed before any password work, and refused only when the
	// submitted name has already failed SoftThreshold times; the answer does not
	// tell a caller whether the name exists.
	if absauth.LoginSaturated(c) &&
		absauth.RefuseSaturatedLogin(c, h.throttle.AccountFailures(accountKey), h.throttle.Delay) {
		absauth.Audit(absauth.AuditEvent{
			Action: "login", Outcome: absauth.OutcomeThrottled, SourceIP: ip,
			Username: username, Reason: "login-ceiling-account-failures",
			Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		})
		absauth.SetRetryAfter(c)
		respondError(c, http.StatusTooManyRequests, "too many failed login attempts — try again later")
		return
	}

	user, err := h.store.GetUserByUsername(username)
	if err != nil {
		// Transient, not a credential verdict.
		absauth.Audit(absauth.AuditEvent{
			Action: "login", Outcome: absauth.OutcomeError, SourceIP: ip, Username: username,
			Reason: "user-lookup-error", Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		})
		respondError(c, http.StatusInternalServerError, "could not verify credentials")
		return
	}
	if user == nil {
		// Charged to the source and the submitted name even for an unknown
		// username, so guessing cannot dodge the hard limit and the name slows
		// down like a real one. The same password work is spent first, so the
		// response time does not give the name away either.
		absauth.SpendVerification(req.Password)
		h.throttle.Delay(h.throttle.RecordFailure(accountKey, throttleKey))
		h.auditLoginFailure(c, ip, username, "", "unknown-user")
		respondError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}

	ok, needsRehash := absauth.VerifyLoginPassword(user.PasswordHashAlgo, user.PasswordHash, req.Password)
	if !ok {
		h.throttle.Delay(h.throttle.RecordFailure(accountKey, throttleKey))
		h.auditLoginFailure(c, ip, username, user.ID, "bad-password")
		respondError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !isActiveUser(user) {
		// A correct password for a disabled account is 403: the credential is fine,
		// the authorization is not. Not counted as a credential failure.
		absauth.Audit(absauth.AuditEvent{
			Action: "login", Outcome: absauth.OutcomeDenied, Mode: servermiddleware.ABSModeJWT,
			SourceIP: ip, Username: username, UserID: user.ID, Reason: "user-inactive",
			Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		})
		respondError(c, http.StatusForbidden, "account is not active")
		return
	}

	// A success clears this account's counter and takes back only this source's own
	// failures against this account (capped), never its failures against others.
	h.throttle.Clear(accountKey, throttleKey)

	// spec §3.5: rehash-on-successful-login migrates bcrypt users to argon2id with no
	// flag day. It is best-effort — a store hiccup here must never deny a correct
	// password, so the error is logged and the login proceeds.
	if needsRehash {
		h.rehashPassword(user, req.Password)
	}

	h.issueSession(c, user, req.DeviceInfo, servermiddleware.ABSModeJWT, "", now)
}

// decodeLoginBody reads the login body without failing on an empty one. An empty body
// is legitimate in Mode C, where the credential is the assertion.
func decodeLoginBody(c *gin.Context) (loginRequest, error) {
	var req loginRequest
	if c.Request == nil || c.Request.Body == nil {
		return req, nil
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return req, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return req, nil
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, err
	}
	return req, nil
}

// rehashPassword re-stores a verified password as argon2id. Best-effort by design.
//
// The write is conditional on the stored hash still being the one just verified
// (RehashUserPassword), and touches only the hash fields of the fresh row. Writing
// back login's copy of the user instead would race a concurrent password change
// (putting the old password back, minus the epoch bump) or deactivation (putting
// Status back to active).
func (h *Handler) rehashPassword(user *database.User, plain string) {
	algo, hash, err := absauth.HashPassword(plain)
	if err != nil {
		absauth.Audit(absauth.AuditEvent{
			Action: "rehash", Outcome: absauth.OutcomeError, UserID: user.ID,
			Username: user.Username, Reason: "hash-failed",
		})
		return
	}
	wrote, err := h.store.RehashUserPassword(user.ID, user.PasswordHash, algo, hash)
	if err != nil {
		absauth.Audit(absauth.AuditEvent{
			Action: "rehash", Outcome: absauth.OutcomeError, UserID: user.ID,
			Username: user.Username, Reason: "store-update-failed",
		})
		return
	}
	if !wrote {
		absauth.Audit(absauth.AuditEvent{
			Action: "rehash", Outcome: absauth.OutcomeDenied, UserID: user.ID,
			Username: user.Username, Reason: "password-changed-concurrently",
		})
		return
	}
	absauth.Audit(absauth.AuditEvent{
		Action: "rehash", Outcome: absauth.OutcomeSuccess, UserID: user.ID,
		Username: user.Username, Reason: "bcrypt-to-argon2id",
	})
}

// issueSession mints a new abs_sess record plus an access/refresh pair and writes the
// login response.
func (h *Handler) issueSession(c *gin.Context, user *database.User, deviceInfo json.RawMessage, mode, email string, now time.Time) {
	ip := strings.TrimSpace(c.ClientIP())

	session, refreshToken, err := h.createSession(user, deviceInfo, ip, c.Request.UserAgent(), now)
	if err != nil {
		absauth.Audit(absauth.AuditEvent{
			Action: "login", Outcome: absauth.OutcomeError, Mode: mode, SourceIP: ip,
			UserID: user.ID, Username: user.Username, Email: email,
			Reason: "session-create-failed", Path: c.Request.URL.Path,
		})
		respondError(c, http.StatusInternalServerError, "could not create session")
		return
	}

	accessToken, _, err := h.cfg.MintAccessToken(user.ID, session.ID, now)
	if err != nil {
		absauth.Audit(absauth.AuditEvent{
			Action: "login", Outcome: absauth.OutcomeError, Mode: mode, SourceIP: ip,
			UserID: user.ID, Username: user.Username, SessionID: session.ID,
			Reason: "mint-access-failed", Path: c.Request.URL.Path,
		})
		respondError(c, http.StatusInternalServerError, "could not issue token")
		return
	}

	progress, bookmarks, err := h.userPayload(user.ID)
	if err != nil {
		// §1.8.1: a 200 with an empty mediaProgress we could not actually read would
		// make AudioBooth delete the user's local positions. 5xx and let it retry.
		absauth.Audit(absauth.AuditEvent{
			Action: "login", Outcome: absauth.OutcomeError, Mode: mode, SourceIP: ip,
			UserID: user.ID, Username: user.Username, SessionID: session.ID,
			Reason: "user-data-unavailable", Path: c.Request.URL.Path,
		})
		respondError(c, http.StatusInternalServerError, "could not load user data")
		return
	}

	absauth.Audit(absauth.AuditEvent{
		Action: "login", Outcome: absauth.OutcomeSuccess, Mode: mode, SourceIP: ip,
		UserID: user.ID, Username: user.Username, Email: email, SessionID: session.ID,
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})

	// x-return-tokens: true asks for the refresh token in the body. We always include
	// it regardless (§1.7.2): omitting refreshToken sets Absorb's isLegacy flag and
	// permanently disables refresh for that server, and AudioBooth needs it too. So
	// the header is honoured as an affirmative in every case.
	respondJSON(c, http.StatusOK, h.buildAuthResponse(user, accessToken, refreshToken, progress, bookmarks))
}

// createSession writes a fresh abs_sess record and returns it with its plaintext
// refresh token (which is returned to the caller and never stored).
//
// The session is stamped with user.CredentialEpoch from the record the caller
// authenticated against: on the password path that is the same copy the password
// was verified with, so a login that raced a password change carries the old
// epoch and is refused once the change lands. It must not be re-read here.
func (h *Handler) createSession(user *database.User, deviceInfo json.RawMessage, ip, userAgent string, now time.Time) (*database.ABSSession, string, error) {
	seed, err := absauth.NewRefreshSeed()
	if err != nil {
		return nil, "", err
	}
	session := &database.ABSSession{
		ID:         h.newID(),
		UserID:     user.ID,
		Seed:       seed,
		Generation: 1,
		DeviceInfo: normalizeDeviceInfo(deviceInfo),
		UserAgent:  userAgent,
		IP:         ip,
		CreatedAt:  now,
		LastUsedAt: now,
		// Sliding: refresh and authenticated use push this forward (see Refresh
		// and ABSIdentityResolver.extendSession). Login is just the first stamp.
		ExpiresAt:       h.cfg.SessionExpiry(now, now),
		CredentialEpoch: user.CredentialEpoch,
	}
	refreshToken := h.cfg.DeriveRefreshToken(session.ID, session.Seed, session.Generation)
	session.RefreshTokenHash = absauth.HashRefreshToken(refreshToken)
	if err := h.store.CreateABSSession(session); err != nil {
		return nil, "", err
	}
	return session, refreshToken, nil
}

// normalizeDeviceInfo stores the client's deviceInfo verbatim when it is valid JSON,
// and drops it otherwise. Stored as text so GET /api/me/sessions can echo the exact
// object the client sent without re-shaping it into a type we would have to guess.
func normalizeDeviceInfo(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if !json.Valid(raw) {
		return ""
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return ""
	}
	return trimmed
}

func (h *Handler) auditLoginFailure(c *gin.Context, ip, username, userID, reason string) {
	absauth.Audit(absauth.AuditEvent{
		Action: "login", Outcome: absauth.OutcomeFailure, Mode: servermiddleware.ABSModeJWT,
		SourceIP: ip, Username: username, UserID: userID, Reason: reason,
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})
}
