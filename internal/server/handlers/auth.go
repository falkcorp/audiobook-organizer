// file: internal/server/handlers/auth.go
// version: 2.20.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678901
// last-edited: 2026-10-21

package handlers

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/gin-gonic/gin"
)

// AuthUserResponse is the JSON shape returned after login and token refresh.
type AuthUserResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Roles     []string  `json:"roles"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	// Permissions is the caller's effective permission set, sorted, so the web UI
	// can hide controls the server would refuse. Only Login and Me fill it: the
	// same struct describes OTHER users in the admin user routes, where a
	// "permissions" key would read as that person's set, so it is omitted there
	// (and omitted for an empty set, which a client reads as none: it defaults an
	// absent field to []). Advisory for the UI only; every route still enforces
	// its own permission.
	Permissions []string `json:"permissions,omitempty"`
}

// AuthUserReader looks up users and counts them for the first-run check.
type AuthUserReader interface {
	GetUserByID(id string) (*database.User, error)
	GetUserByUsername(username string) (*database.User, error)
	CountUsers() (int, error)
}

// AuthUserWriter creates and updates users. UpdateUser writes ordinary fields
// (the profile email); the password changes only through the
// database.UserCredentialWriter capability (AuthHandler.creds), which bumps the
// credential epoch on the fresh row.
type AuthUserWriter interface {
	CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*database.User, error)
	UpdateUser(user *database.User) error
}

// AuthRoleReader resolves a role by id. Login needs it to report the user's
// effective permissions before any middleware has bound them to a request.
type AuthRoleReader interface {
	GetRoleByID(id string) (*database.Role, error)
}

// AuthSessionStore issues, reads, lists and revokes sessions.
type AuthSessionStore interface {
	CreateSession(userID, ip, userAgent string, ttl time.Duration) (*database.Session, error)
	CreateSessionWithOrigin(userID, ip, userAgent string, ttl time.Duration, origin string) (*database.Session, error)
	GetSession(id string) (*database.Session, error)
	ListUserSessions(userID string) ([]database.Session, error)
	RevokeSession(id string) error
}

// AuthStore is the narrow database interface AuthHandler requires.
// Only the methods actually called by the handler are listed here.
//
// Split into interfaces on 2026-08-18. This name is retained as their
// composition so no consumer moves. AuthRoleReader was dropped on 2026-10-07
// (ChangePassword's role lookups were replaced by the request's effective
// permissions, auth.Can) and returned on 2026-10-18 for Login alone, which
// answers before the auth middleware has bound any permissions.
type AuthStore interface {
	AuthUserReader
	AuthUserWriter
	AuthRoleReader
	AuthSessionStore
}

const (
	// Per-IP failed-login throttle. Once a single source IP exceeds
	// maxFailedLoginsPerIP failures inside loginThrottleWindow it is rejected
	// with 429 until the window rolls over. Keyed on the *attacker's* source —
	// not the target account — so it cannot be used to lock a victim out
	// (pen-test finding HIGH-3). This is meaningful now that X-Forwarded-For is
	// no longer trusted for ClientIP (HIGH-2). "Source" is the tunnel visitor
	// where the service can establish one (throttleKey), not the tunnel
	// connector every visitor shares.
	maxFailedLoginsPerIP = 15
	loginThrottleWindow  = 15 * time.Minute

	// Per-account soft slowdown. After accountSoftThreshold failures a small
	// progressive delay (capped at accountSoftMaxDelay) is added to each *failed*
	// attempt. The account is never hard-locked: a correct password always
	// succeeds immediately, so a third party cannot deny a known user access.
	accountSoftThreshold = 5
	accountSoftStep      = 200 * time.Millisecond
	accountSoftMaxDelay  = 2 * time.Second

	// DefaultSessionTTL is the session lifetime for a normal login.
	DefaultSessionTTL = 24 * time.Hour
	// TempLoginTokenTTL is the lifetime of a single-use temp-login token.
	TempLoginTokenTTL = 15 * time.Minute

	rememberMeSessionTTL = 7 * 24 * time.Hour // unexported; only used within this package
	defaultSessionTTL    = DefaultSessionTTL
)

type failedAttempt struct {
	count   int
	firstAt time.Time
}

// AuthHandler handles all /auth routes (login, sessions, password management).
type AuthHandler struct {
	store      AuthStore
	enableAuth bool
	acctFails  map[string]*failedAttempt // keyed by user ID — drives the soft delay
	ipFails    map[string]*failedAttempt // keyed by client IP — drives the hard throttle
	failMu     sync.Mutex
	// failureDelay performs the soft per-account slowdown. Defaults to time.Sleep;
	// tests override it to keep the suite fast and deterministic.
	failureDelay func(time.Duration)
	// absSessions signs a user out of every ABS client when their password
	// changes. nil when the store has no ABS keyspace (no ABS session can exist).
	absSessions ABSSessionRevoker
	// creds is the only path that changes a password (SetUserPassword bumps the
	// credential epoch with it). nil when the store lacks the capability, in
	// which case ChangePassword refuses.
	creds database.UserCredentialWriter
	// revokeGuestAccess ends invite-granted SSO / Cloudflare access for an
	// email the user gave up in UpdateMe; nil = nothing to revoke.
	revokeGuestAccess GuestAccessRevoker
	// throttleKeyFn names the bucket of the per-IP failure throttle for a
	// request (SetThrottleKeyFunc); nil keys on c.ClientIP().
	throttleKeyFn func(*gin.Context) string
}

// NewAuthHandler constructs an AuthHandler.
// enableAuth should be set from config.AppConfig.EnableAuth at wire time.
func NewAuthHandler(store AuthStore, enableAuth bool) *AuthHandler {
	return &AuthHandler{
		store:        store,
		enableAuth:   enableAuth,
		acctFails:    make(map[string]*failedAttempt),
		ipFails:      make(map[string]*failedAttempt),
		failureDelay: time.Sleep,
		absSessions:  ResolveABSSessionRevoker(store),
		creds:        ResolveUserCredentialWriter(store),
	}
}

// SetThrottleKeyFunc sets how the per-IP failed-login throttle keys a request.
// The router trusts no proxy headers, so behind the Cloudflare tunnel c.ClientIP()
// is the connector's address, shared by every visitor: three buckets for the
// whole site, and one caller spending one of them locks out everyone behind
// that connector. The server wires a function that returns the visitor's own
// address when that can be believed (see server.loginThrottleKey) and the peer's
// address otherwise. nil, or an empty answer, keeps c.ClientIP(). It decides
// only the throttle bucket; sessions still record c.ClientIP().
func (h *AuthHandler) SetThrottleKeyFunc(fn func(*gin.Context) string) {
	h.throttleKeyFn = fn
}

// throttleKey is the bucket the failed-login throttle charges this request to.
func (h *AuthHandler) throttleKey(c *gin.Context) string {
	if h.throttleKeyFn != nil {
		if k := strings.TrimSpace(h.throttleKeyFn(c)); k != "" {
			return k
		}
	}
	return strings.TrimSpace(c.ClientIP())
}

// SetFailureDelay replaces the per-account soft-delay function. The only
// intended caller is test code in the external handlers_test package; inject a
// no-op (func(time.Duration) {}) to keep the suite fast and deterministic.
func (h *AuthHandler) SetFailureDelay(fn func(time.Duration)) {
	h.failureDelay = fn
}

// bumpFailureLocked increments the windowed failure counter for key and returns
// the post-increment count. Caller must hold failMu.
func bumpFailureLocked(m map[string]*failedAttempt, key string) int {
	a, ok := m[key]
	if !ok || time.Since(a.firstAt) > loginThrottleWindow {
		m[key] = &failedAttempt{count: 1, firstAt: time.Now()}
		return 1
	}
	a.count++
	return a.count
}

// ipThrottled reports whether the given source IP has exceeded its failed-login
// budget within the current window.
func (h *AuthHandler) ipThrottled(ip string) bool {
	if ip == "" {
		return false
	}
	h.failMu.Lock()
	defer h.failMu.Unlock()
	a, ok := h.ipFails[ip]
	if !ok {
		return false
	}
	if time.Since(a.firstAt) > loginThrottleWindow {
		delete(h.ipFails, ip)
		return false
	}
	return a.count >= maxFailedLoginsPerIP
}

// purgeExpiredLocked removes entries whose throttle window has elapsed so the
// maps don't grow without bound when attackers rotate source IPs. Caller must
// hold failMu.
func purgeExpiredLocked(m map[string]*failedAttempt) {
	for k, a := range m {
		if time.Since(a.firstAt) > loginThrottleWindow {
			delete(m, k)
		}
	}
}

// recordFailure bumps the per-IP counter and (when userID is non-empty) the
// per-account counter, returning the soft delay to apply for this account
// failure. Unknown users still count against the IP so username guessing can't
// dodge the throttle.
func (h *AuthHandler) recordFailure(userID, ip string) time.Duration {
	h.failMu.Lock()
	defer h.failMu.Unlock()
	purgeExpiredLocked(h.ipFails)
	purgeExpiredLocked(h.acctFails)
	if ip != "" {
		bumpFailureLocked(h.ipFails, ip)
	}
	if userID == "" {
		return 0
	}
	count := bumpFailureLocked(h.acctFails, userID)
	if count <= accountSoftThreshold {
		return 0
	}
	d := min(time.Duration(count-accountSoftThreshold)*accountSoftStep, accountSoftMaxDelay)
	return d
}

// clearFailures resets both counters after a successful login.
func (h *AuthHandler) clearFailures(userID, ip string) {
	h.failMu.Lock()
	defer h.failMu.Unlock()
	delete(h.acctFails, userID)
	if ip != "" {
		delete(h.ipFails, ip)
	}
}

// buildAuthUserResponse converts a database User to the API response shape.
// It deliberately omits sensitive fields (password_hash, password_hash_algo)
// so they can never leak into a JSON response.
func buildAuthUserResponse(user *database.User) AuthUserResponse {
	return AuthUserResponse{
		ID:        user.ID,
		Username:  user.Username,
		Email:     user.Email,
		Roles:     user.Roles,
		Status:    user.Status,
		CreatedAt: user.CreatedAt,
	}
}

// permissionStrings flattens a permission set into a sorted slice, [] when
// empty.
func permissionStrings(perms []auth.Permission) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, string(p))
	}
	sort.Strings(out)
	return out
}

// BuildAuthUserResponse is the exported form of buildAuthUserResponse for
// callers in sibling packages (e.g. the server package's accept-invite handler).
// Always use this instead of returning a raw *database.User, which would leak
// the password hash (pen-test finding CRIT-2).
func BuildAuthUserResponse(user *database.User) AuthUserResponse {
	return buildAuthUserResponse(user)
}

func isHTTPSRequest(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https")
}

// SetSessionCookie writes a session cookie to the response.
func SetSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	setSessionCookie(c, token, expiresAt)
}

// setSessionCookie writes a session cookie to the response.
func setSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	if c == nil {
		return
	}
	maxAge := max(int(time.Until(expiresAt).Seconds()), 0)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     servermiddleware.SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   isHTTPSRequest(c),
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie clears the session cookie.
func clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     servermiddleware.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPSRequest(c),
		SameSite: http.SameSiteStrictMode,
	})
}

// GetStatus handles GET /auth/status.
func (h *AuthHandler) GetStatus(c *gin.Context) {
	count, err := h.store.CountUsers()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to read auth status")
		return
	}
	requiresAuth := h.enableAuth && count > 0
	httputil.RespondWithOK(c, gin.H{
		"has_users":       count > 0,
		"auth_enabled":    h.enableAuth,
		"requires_auth":   requiresAuth,
		"bootstrap_ready": h.enableAuth && count == 0,
	})
}

// SetupInitialAdmin handles POST /auth/setup.
func (h *AuthHandler) SetupInitialAdmin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Email    string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)
	if req.Username == "" || len(req.Password) < 8 {
		httputil.RespondWithBadRequest(c, "username and password (min 8 chars) are required")
		return
	}
	if req.Email == "" {
		req.Email = req.Username + "@local"
	}
	count, err := h.store.CountUsers()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to check existing users")
		return
	}
	if count > 0 {
		httputil.RespondWithConflict(c, "initial setup already completed")
		return
	}
	algo, hash, err := absauth.HashPassword(req.Password)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to hash password")
		return
	}
	created, err := h.store.CreateUser(req.Username, req.Email, algo, hash, []string{"admin"}, "active")
	if err != nil {
		httputil.RespondWithBadRequest(c, "failed to create initial user")
		return
	}
	httputil.RespondWithCreated(c, gin.H{
		"message": "admin user created",
		"user":    buildAuthUserResponse(created),
	})
}

// Login handles POST /auth/login.
func (h *AuthHandler) Login(c *gin.Context) {
	var req struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		RememberMe bool   `json:"remember_me"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		httputil.RespondWithBadRequest(c, "username and password are required")
		return
	}
	ip := h.throttleKey(c)
	// Per-IP throttle first: a source that has burned through its failure budget
	// is rejected before any credential work, so it can't keep probing (HIGH-3).
	if h.ipThrottled(ip) {
		httputil.RespondWithError(c, http.StatusTooManyRequests, "too many failed login attempts from this source — try again later", "TOO_MANY_REQUESTS")
		return
	}
	user, err := h.store.GetUserByUsername(req.Username)
	if err != nil || user == nil {
		// Count the failure against the IP even for unknown users so username
		// guessing can't dodge the throttle.
		h.recordFailure("", ip)
		httputil.RespondWithUnauthorized(c, "invalid credentials")
		return
	}
	if ok, _ := absauth.VerifyPassword(user.PasswordHashAlgo, user.PasswordHash, req.Password); !ok {
		// Soft, progressive per-account slowdown — never a hard lock, so the
		// real user with the correct password is never denied (HIGH-3).
		if delay := h.recordFailure(user.ID, ip); delay > 0 {
			h.failureDelay(delay)
		}
		httputil.RespondWithUnauthorized(c, "invalid credentials")
		return
	}
	h.clearFailures(user.ID, ip)
	ttl := defaultSessionTTL
	if req.RememberMe {
		ttl = rememberMeSessionTTL
	}
	// The user's own password sign-in: the one session origin (with OAuth)
	// that counts as an interactive login (auth.MethodSession).
	session, err := h.store.CreateSessionWithOrigin(
		user.ID,
		strings.TrimSpace(c.ClientIP()),
		strings.TrimSpace(c.Request.UserAgent()),
		ttl,
		database.SessionOriginPassword,
	)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to create session")
		return
	}
	setSessionCookie(c, session.ID, session.ExpiresAt)
	// The session token lives only in the HttpOnly cookie set above. Do NOT
	// return session.ID in the JSON body — that would expose the bearer token
	// to page JavaScript and defeat the HttpOnly protection against XSS theft.
	// Expose non-authenticating metadata only (expiry) for UI display.
	// Login answers before the auth middleware binds permissions, so they are
	// computed from the user's roles with the middleware's own union
	// (EffectivePermissionsFor). A password sign-in is never key-scoped, so this
	// is exactly the set a request on the new session will carry.
	resp := buildAuthUserResponse(user)
	resp.Permissions = permissionStrings(servermiddleware.EffectivePermissionsFor(h.store, user))
	httputil.RespondWithOK(c, gin.H{
		"user":       resp,
		"expires_at": session.ExpiresAt,
	})
}

// Me handles GET /auth/me.
func (h *AuthHandler) Me(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok {
		httputil.RespondWithUnauthorized(c, "not authenticated")
		return
	}
	// The request's effective permissions, the set auth.Can reads: the role union
	// the middleware bound, already narrowed by an API key's scopes when the
	// request came in on a key. A fresh walk of the roles here would report
	// permissions a scoped key cannot actually use.
	resp := buildAuthUserResponse(user)
	resp.Permissions = permissionStrings(requestPermissions(c))
	httputil.RespondWithOK(c, gin.H{"user": resp})
}

// requestPermissions lists the permissions bound to c's request context.
func requestPermissions(c *gin.Context) []auth.Permission {
	set := auth.PermissionsFromContext(c.Request.Context())
	out := make([]auth.Permission, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	return out
}

// UpdateMe handles PATCH /auth/me.
func (h *AuthHandler) UpdateMe(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok {
		httputil.RespondWithUnauthorized(c, "not authenticated")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	email := strings.TrimSpace(req.Email)
	if email == "" {
		httputil.RespondWithBadRequest(c, "email is required")
		return
	}
	oldEmail := user.Email
	user.Email = email
	if err := h.store.UpdateUser(user); err != nil {
		if errors.Is(err, database.ErrUserProtectedField) {
			// The copy was read before a password or status change landed.
			httputil.RespondWithConflict(c, "the account changed while the profile was being saved; reload and try again")
			return
		}
		if errors.Is(err, database.ErrUserNotFound) {
			// Defensive: nothing deletes users today, but the store reports a
			// missing row as ErrUserNotFound, and that is a 404, not a 500.
			httputil.RespondWithNotFound(c, "user", user.ID)
			return
		}
		httputil.RespondWithInternalError(c, "failed to update profile")
		return
	}
	// The old address no longer belongs to this account, so neither does any
	// SSO or Cloudflare access an accepted invite granted to it.
	if h.revokeGuestAccess != nil && oldEmail != "" && !util.SameEmail(oldEmail, email) {
		h.revokeGuestAccess(user.ID, oldEmail, "email changed")
	}
	httputil.RespondWithOK(c, gin.H{"user": buildAuthUserResponse(user)})
}

// Logout handles POST /auth/logout.
func (h *AuthHandler) Logout(c *gin.Context) {
	session, ok := servermiddleware.CurrentSession(c)
	if ok && session != nil {
		_ = h.store.RevokeSession(session.ID)
	}
	clearSessionCookie(c)
	httputil.RespondWithOK(c, gin.H{"message": "logged out"})
}

// ListMySessions handles GET /auth/sessions.
func (h *AuthHandler) ListMySessions(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok {
		httputil.RespondWithUnauthorized(c, "not authenticated")
		return
	}
	currentSession, _ := servermiddleware.CurrentSession(c)
	sessions, err := h.store.ListUserSessions(user.ID)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to list sessions")
		return
	}
	type sessionView struct {
		database.Session
		Current bool `json:"current"`
	}
	response := make([]sessionView, 0, len(sessions))
	for _, s := range sessions {
		response = append(response, sessionView{
			Session: s,
			Current: currentSession != nil && s.ID == currentSession.ID,
		})
	}
	httputil.RespondWithOK(c, gin.H{"sessions": response})
}

// RevokeMySession handles DELETE /auth/sessions/:id.
func (h *AuthHandler) RevokeMySession(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok {
		httputil.RespondWithUnauthorized(c, "not authenticated")
		return
	}
	currentSession, _ := servermiddleware.CurrentSession(c)
	sessionID := strings.TrimSpace(c.Param("id"))
	if sessionID == "" {
		httputil.RespondWithBadRequest(c, "session id required")
		return
	}
	targetSession, err := h.store.GetSession(sessionID)
	if err != nil || targetSession == nil {
		httputil.RespondWithNotFound(c, "session", sessionID)
		return
	}
	if targetSession.UserID != user.ID {
		httputil.RespondWithForbidden(c, "cannot revoke another user's session")
		return
	}
	if err := h.store.RevokeSession(sessionID); err != nil {
		httputil.RespondWithInternalError(c, "failed to revoke session")
		return
	}
	if currentSession != nil && currentSession.ID == sessionID {
		clearSessionCookie(c)
	}
	httputil.RespondWithNoContent(c)
}

// ChangePassword handles PUT /auth/me/password.
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	caller, _ := servermiddleware.CurrentUser(c)
	if caller == nil {
		httputil.RespondWithUnauthorized(c, "not authenticated")
		return
	}
	// API keys are refused at the route (server.credRoute), the one place
	// every credential-changing route is guarded and recorded.
	var req struct {
		UserID          string `json:"user_id"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.NewPassword) < 8 {
		httputil.RespondWithBadRequest(c, "new password must be at least 8 characters")
		return
	}
	targetID := caller.ID
	isAdminReset := false
	if req.UserID != "" && req.UserID != caller.ID {
		// The request's effective permissions (the auth middleware's union of
		// the caller's roles), not a fresh walk of the user's roles: the walk
		// ignored everything about how the request was authenticated.
		if !auth.Can(c.Request.Context(), auth.PermUsersManage) {
			httputil.RespondWithForbidden(c, "only admins can reset another user's password")
			return
		}
		targetID = req.UserID
		isAdminReset = true
	}
	target, err := h.store.GetUserByID(targetID)
	if err != nil || target == nil {
		httputil.RespondWithNotFound(c, "user", targetID)
		return
	}
	if !isAdminReset {
		if req.CurrentPassword == "" {
			httputil.RespondWithBadRequest(c, "current_password is required")
			return
		}
		if ok, _ := absauth.VerifyPassword(target.PasswordHashAlgo, target.PasswordHash, req.CurrentPassword); !ok {
			httputil.RespondWithUnauthorized(c, "current password is incorrect")
			return
		}
	}
	newAlgo, newHash, err := absauth.HashPassword(req.NewPassword)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to hash password")
		return
	}
	// SetUserPassword writes the new hash and bumps CredentialEpoch on the FRESH
	// row in one write. Every ABS session stamped with an earlier epoch is
	// refused from that instant, including one minted with the old password
	// between our read of target and this write; a revoke issued before the
	// write could not reach that one. A self-service change passes the hash it
	// verified the current password against, so it lands only if that is still
	// the password (ErrUserStale otherwise); an admin reset verifies nothing and
	// passes "".
	if h.creds == nil {
		httputil.RespondWithInternalError(c, "password changes are not supported by this store")
		return
	}
	expected := ""
	if !isAdminReset {
		expected = target.PasswordHash
	}
	if _, err := h.creds.SetUserPassword(target.ID, expected, newAlgo, newHash); err != nil {
		switch {
		case errors.Is(err, database.ErrUserStale):
			httputil.RespondWithConflict(c, "the password changed while this request was in flight; nothing was changed, try again")
		case errors.Is(err, database.ErrUserNotFound):
			httputil.RespondWithNotFound(c, "user", target.ID)
		default:
			httputil.RespondWithInternalError(c, "failed to update password")
		}
		return
	}
	reason := "password-changed"
	if isAdminReset {
		reason = "admin-password-reset"
	}
	RevokeAllABSSessions(h.absSessions, target.ID, reason)
	httputil.RespondWithNoContent(c)
}
