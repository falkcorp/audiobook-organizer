// file: internal/server/middleware/owner_guard.go
// version: 1.0.0
// guid: 8c4f2e17-9a63-4d05-b1e8-6f3a7d2c5b90
// last-edited: 2026-10-07

package middleware

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// OwnerPublicHost is the host the browser used, for the owner sign-in hint:
// the first X-Forwarded-Host behind a proxy, else Host. Display only; it
// never decides anything.
func OwnerPublicHost(r *http.Request) string {
	if h := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); h != "" {
		if i := strings.IndexByte(h, ','); i >= 0 {
			h = strings.TrimSpace(h[:i])
		}
		return h
	}
	return r.Host
}

// RequireOwner refuses the route unless the request is the owner
// (auth.OwnerProofWhyNot: a verified Cloudflare Access JWT whose email is the
// configured owner_email; an API key, a password, SSO, temp-login or invite
// session never is) AND holds perm. ownerEmail is read on every request so an
// owner_email change takes effect at once; nil or "" refuses everyone.
//
// applies, when non-nil, limits the gate to the requests it reports true for
// (the apply half of a route whose preview stays open). It must read the
// request exactly as the handler does (the same helper, not a copy), and
// anything it cannot read as a preview must report true.
//
// It is NOT skipped when auth is disabled, unlike s.perm: the owner proof is
// a Cloudflare Access identity, which exists whether or not local sign-in is
// on, and an install with auth off has no owner to fall back to.
func RequireOwner(ownerEmail func() string, perm auth.Permission, applies func(*gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if applies != nil && !applies(c) {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		email := ""
		if ownerEmail != nil {
			email = ownerEmail()
		}
		userID := ""
		if u, ok := auth.UserFromContext(ctx); ok && u != nil {
			userID = u.ID
		}
		if why := auth.OwnerProofWhyNot(ctx, email, OwnerPublicHost(c.Request)); why != "" {
			slog.Warn("owner action refused", "route", c.FullPath(), "http_method", c.Request.Method,
				"method", string(auth.MethodFromContext(ctx)), "user", userID, "why", why)
			httputil.RespondWithForbidden(c, why)
			c.Abort()
			return
		}
		if !auth.Can(ctx, perm) {
			slog.Warn("owner action refused", "route", c.FullPath(), "http_method", c.Request.Method,
				"user", userID, "why", "missing permission "+string(perm))
			httputil.RespondWithForbidden(c, "permission denied: "+string(perm))
			c.Abort()
			return
		}
		// The audit line for every owner action: who, proven how, did what.
		slog.Info("owner action allowed", "route", c.FullPath(), "http_method", c.Request.Method,
			"user", userID, "access_email", auth.AccessEmailFromContext(ctx),
			"method", string(auth.MethodFromContext(ctx)), "query", c.Request.URL.RawQuery)
		c.Next()
	}
}
