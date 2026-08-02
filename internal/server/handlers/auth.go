// file: internal/server/handlers/auth.go
// version: 2.4.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678901
// last-edited: 2026-06-09

package handlers

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// AuthUserResponse is the JSON shape returned after login and token refresh.
type AuthUserResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Roles     []string  `json:"roles"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// AuthStore is the narrow database interface AuthHandler requires.
// Only the methods actually called by the handler are listed here.
type AuthStore interface {
	CountUsers() (int, error)
	CreateSession(userID, ip, userAgent string, ttl time.Duration) (*database.Session, error)
	CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*database.User, error)
	GetRoleByID(id string) (*database.Role, error)
	GetRoleByName(name string) (*database.Role, error)
	GetSession(id string) (*database.Session, error)
	GetUserByID(id string) (*database.User, error)
	GetUserByUsername(username string) (*database.User, error)
	ListUserSessions(userID string) ([]database.Session, error)
	RevokeSession(id string) error
	UpdateUser(user *database.User) error
}

const (
	// Per-IP failed-login throttle. Once a single source IP exceeds
	// maxFailedLoginsPerIP failures inside loginThrottleWindow it is rejected
	// with 429 until the window rolls over. Keyed on the *attacker's* source —
	// not the target account — so it cannot be used to lock a victim out
	// (pen-test finding HIGH-3). This is meaningful now that X-Forwarded-For is
	// no longer trusted for ClientIP (HIGH-2).
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
	}
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
	d := time.Duration(count-accountSoftThreshold) * accountSoftStep
	if d > accountSoftMaxDelay {
		d = accountSoftMaxDelay
	}
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

// BuildAuthUserResponse is the exported form of buildAuthUserResponse for
// callers in sibling packages (e.g. the server package's accept-invite handler).
// Always use this instead of returning a raw *database.User, which would leak
// the bcrypt password hash (pen-test finding CRIT-2).
func BuildAuthUserResponse(user *database.User) AuthUserResponse {
	return buildAuthUserResponse(user)
}

func isHTTPSRequest(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https")
}

// IsHTTPSRequest reports whether the inbound request is HTTPS.
// Exported for callers in sibling packages (e.g. auth_temp_login.go).
func IsHTTPSRequest(c *gin.Context) bool { return isHTTPSRequest(c) }

// SetSessionCookie writes a session cookie to the response.
func SetSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	setSessionCookie(c, token, expiresAt)
}

// setSessionCookie writes a session cookie to the response.
func setSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	if c == nil {
		return
	}
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
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
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to hash password")
		return
	}
	created, err := h.store.CreateUser(req.Username, req.Email, "bcrypt", string(hash), []string{"admin"}, "active")
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
	ip := strings.TrimSpace(c.ClientIP())
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
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
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
	session, err := h.store.CreateSession(
		user.ID,
		strings.TrimSpace(c.ClientIP()),
		strings.TrimSpace(c.Request.UserAgent()),
		ttl,
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
	httputil.RespondWithOK(c, gin.H{
		"user":       buildAuthUserResponse(user),
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
	httputil.RespondWithOK(c, gin.H{"user": buildAuthUserResponse(user)})
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
	user.Email = email
	if err := h.store.UpdateUser(user); err != nil {
		httputil.RespondWithInternalError(c, "failed to update profile")
		return
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
		isAdmin := false
		for _, roleRef := range caller.Roles {
			r, _ := h.store.GetRoleByName(roleRef)
			if r == nil {
				r, _ = h.store.GetRoleByID(roleRef)
			}
			if r == nil {
				continue
			}
			for _, p := range r.Permissions {
				if p == "users.manage" {
					isAdmin = true
				}
			}
		}
		if !isAdmin {
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
		if err := bcrypt.CompareHashAndPassword([]byte(target.PasswordHash), []byte(req.CurrentPassword)); err != nil {
			httputil.RespondWithUnauthorized(c, "current password is incorrect")
			return
		}
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to hash password")
		return
	}
	target.PasswordHash = string(newHash)
	target.PasswordHashAlgo = "bcrypt"
	if err := h.store.UpdateUser(target); err != nil {
		httputil.RespondWithInternalError(c, "failed to update password")
		return
	}
	httputil.RespondWithNoContent(c)
}
