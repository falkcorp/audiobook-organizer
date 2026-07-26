// file: internal/server/middleware/auth.go
// version: 1.5.3
// guid: 83c42ecb-1df2-4baf-9890-3f91ab4db6fe
// last-edited: 2026-07-26

package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

const (
	// SessionCookieName is the auth session cookie used by API clients.
	SessionCookieName = "session_id"
	contextUserKey    = "auth_user"
	contextSessionKey = "auth_session"
	contextAPIKeyKey  = "auth_api_key"
)

// SessionTokenFromRequest extracts the session token from Bearer auth or cookie.
func SessionTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		token := strings.TrimSpace(authHeader[len("Bearer "):])
		if token != "" {
			return token
		}
	}
	if cookie, err := r.Cookie(SessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		return strings.TrimSpace(cookie.Value)
	}
	return ""
}

// CurrentUser fetches the authenticated user from Gin context.
func CurrentUser(c *gin.Context) (*database.User, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.Get(contextUserKey)
	if !ok || value == nil {
		return nil, false
	}
	user, ok := value.(*database.User)
	return user, ok && user != nil
}

// CurrentSession fetches the authenticated session from Gin context.
func CurrentSession(c *gin.Context) (*database.Session, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.Get(contextSessionKey)
	if !ok || value == nil {
		return nil, false
	}
	session, ok := value.(*database.Session)
	return session, ok && session != nil
}

// CurrentAPIKey fetches the API key that authenticated this request, if any.
func CurrentAPIKey(c *gin.Context) (*database.APIKey, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.Get(contextAPIKeyKey)
	if !ok || value == nil {
		return nil, false
	}
	key, ok := value.(*database.APIKey)
	return key, ok && key != nil
}

// RequireAuth enforces session-based auth when at least one user exists.
// Tokens prefixed with "abk_" are routed through API key validation;
// all other tokens fall through to session validation.
func RequireAuth(store interface {
	database.UserReader
	database.RoleStore
	database.SessionStore
	database.APIKeyStore
}) gin.HandlerFunc {
	return func(c *gin.Context) {
		if store == nil {
			c.Next()
			return
		}

		userCount, err := store.CountUsers()
		if err != nil {
			httputil.RespondWithInternalError(c, "failed to check auth state")
			c.Abort()
			return
		}
		if userCount == 0 {
			// First-run bootstrap mode: setup endpoint can create the first admin.
			c.Next()
			return
		}

		// An earlier stage (e.g. CloudflareAccessAuth) may have already resolved and
		// bound the user for this request; if so, don't require a session token.
		if u, ok := c.Get(contextUserKey); ok && u != nil {
			c.Next()
			return
		}

		token := SessionTokenFromRequest(c.Request)
		if token == "" {
			httputil.RespondWithUnauthorized(c, "authentication required")
			c.Abort()
			return
		}

		if strings.HasPrefix(token, "abk_") {
			handleAPIKeyAuth(c, store, token)
			return
		}

		session, err := store.GetSession(token)
		if err != nil || session == nil {
			httputil.RespondWithUnauthorized(c, "invalid session")
			c.Abort()
			return
		}
		if session.Revoked || time.Now().After(session.ExpiresAt) {
			_ = store.RevokeSession(token)
			httputil.RespondWithUnauthorized(c, "session expired")
			c.Abort()
			return
		}

		user, err := store.GetUserByID(session.UserID)
		if err != nil || user == nil {
			httputil.RespondWithUnauthorized(c, "invalid session user")
			c.Abort()
			return
		}
		if status := strings.ToLower(strings.TrimSpace(user.Status)); status != "" && status != "active" {
			httputil.RespondWithUnauthorized(c, "inactive user")
			c.Abort()
			return
		}

		c.Set(contextUserKey, user)
		c.Set(contextSessionKey, session)

		// Attach user + effective permissions on the request context using the
		// typed helpers from internal/auth so handlers can call auth.Can(ctx, perm).
		perms := effectivePermissionsFor(store, user)
		ctx := auth.WithUser(c.Request.Context(), user)
		ctx = auth.WithPermissions(ctx, perms)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// handleAPIKeyAuth validates an "abk_" prefixed token and, on success, binds
// the user and scoped permissions to the context then calls c.Next().
func handleAPIKeyAuth(c *gin.Context, store interface {
	database.UserReader
	database.RoleStore
	database.APIKeyStore
}, rawToken string) {
	hash := database.HashAPIKeyToken(rawToken)
	key, err := store.GetAPIKeyByHash(hash)
	if err != nil {
		slog.Info("lookup error hash err", "hashPrefix", hash[:8], "err", err)
		httputil.RespondWithInternalError(c, "internal error")
		c.Abort()
		return
	}
	if key == nil {
		httputil.RespondWithUnauthorized(c, "invalid API key")
		c.Abort()
		return
	}
	if key.Status == "revoked" {
		httputil.RespondWithUnauthorized(c, "API key has been revoked")
		c.Abort()
		return
	}
	if key.Status == "inactive" {
		httputil.RespondWithUnauthorized(c, "API key is inactive")
		c.Abort()
		return
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		httputil.RespondWithUnauthorized(c, "API key has expired")
		c.Abort()
		return
	}

	user, err := store.GetUserByID(key.UserID)
	if err != nil || user == nil {
		httputil.RespondWithUnauthorized(c, "invalid API key owner")
		c.Abort()
		return
	}
	if status := strings.ToLower(strings.TrimSpace(user.Status)); status != "" && status != "active" {
		httputil.RespondWithUnauthorized(c, "inactive user")
		c.Abort()
		return
	}

	// Compute user's role-based permissions, then narrow to key scopes.
	rolePerms := effectivePermissionsFor(store, user)
	effectivePerms := intersectPermissions(rolePerms, key.Scopes)

	ip := c.ClientIP()
	go func() {
		if touchErr := store.TouchAPIKeyLastUsed(key.ID, time.Now(), ip); touchErr != nil {
			slog.Info("touch error id err", "key", key.ID, "touchErr", touchErr)
		}
	}()

	c.Set(contextUserKey, user)
	c.Set(contextAPIKeyKey, key)

	ctx := auth.WithUser(c.Request.Context(), user)
	ctx = auth.WithPermissions(ctx, effectivePerms)
	c.Request = c.Request.WithContext(ctx)

	c.Next()
}

// intersectPermissions returns only permissions that appear in both rolePerms
// and scopes. The key can only narrow, never expand, user role permissions.
func intersectPermissions(rolePerms []auth.Permission, scopes []string) []auth.Permission {
	if len(scopes) == 0 {
		return rolePerms
	}
	scopeSet := make(map[auth.Permission]bool, len(scopes))
	for _, s := range scopes {
		scopeSet[auth.Permission(s)] = true
	}
	var out []auth.Permission
	for _, p := range rolePerms {
		if scopeSet[p] {
			out = append(out, p)
		}
	}
	return out
}

// effectivePermissionsFor resolves the union of every role's
// Permissions slice for the given user. Unknown roles are skipped.
// Returns nil if the user has no roles (which makes every Can()
// check return false — safe default).
func effectivePermissionsFor(store database.RoleStore, user *database.User) []auth.Permission {
	if user == nil || len(user.Roles) == 0 || store == nil {
		return nil
	}
	seen := make(map[auth.Permission]struct{})
	for _, roleID := range user.Roles {
		role, err := store.GetRoleByID(roleID)
		if err != nil || role == nil {
			// PebbleDB is the only backend and always resolves roles; the former
			// SQLite-RBAC-unsupported warning branch was permanently dead and has
			// been removed (HIGH-4b no longer reachable).
			continue
		}
		for _, p := range role.Permissions {
			seen[p] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]auth.Permission, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out
}

// RequirePermission returns a middleware that aborts with 403 if the
// calling user doesn't have permission p. Must be chained after
// RequireAuth (which loads the user + permission set into the
// request context).
//
// Exception: if no users exist yet (first-run bootstrap), the check
// is bypassed so the /setup wizard can run unauthenticated.
func RequirePermission(store interface {
	database.UserReader
	database.RoleStore
	database.SessionStore
	database.APIKeyStore
}, p auth.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		// First-run bypass — RequireAuth uses the same pattern.
		if store != nil {
			if n, err := store.CountUsers(); err == nil && n == 0 {
				c.Next()
				return
			}
		}
		if !auth.Can(c.Request.Context(), p) {
			// Distinguish unauth (no user on ctx) from authz fail
			// (user present but missing perm) so API clients get a
			// meaningful status code.
			if _, ok := auth.UserFromContext(c.Request.Context()); !ok {
				httputil.RespondWithUnauthorized(c, "authentication required")
			} else {
				httputil.RespondWithForbidden(c, "permission denied: "+string(p))
			}
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireAdmin is a middleware that checks the authenticated user has the "admin" role.
// Must be used after RequireAuth in the middleware chain.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := CurrentUser(c)
		if !ok || user == nil {
			httputil.RespondWithUnauthorized(c, "authentication required")
			c.Abort()
			return
		}
		for _, role := range user.Roles {
			if role == "admin" {
				c.Next()
				return
			}
		}
		httputil.RespondWithForbidden(c, "admin role required")
		c.Abort()
	}
}
