// file: internal/server/middleware/owner_guard.go
// version: 1.2.0
// guid: 8c4f2e17-9a63-4d05-b1e8-6f3a7d2c5b90
// last-edited: 2026-10-09

package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ownerGuardLog carries this file's diagnostics through the log-injection barrier.
var ownerGuardLog = logger.New("http")

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
// perm "" skips the permission leg only (the server passes "" when local
// auth is off, exactly as s.perm turns into a no-op: there are no roles to
// hold it). The owner proof is NEVER skipped, auth on or off: it is a
// Cloudflare Access identity, which exists whether or not local sign-in is
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
			ownerGuardLog.Warn("owner action refused: route=%s http_method=%s method=%s user=%v why=%s",
				c.FullPath(), c.Request.Method,
				string(auth.MethodFromContext(ctx)), userID, why)
			httputil.RespondWithForbidden(c, why)
			c.Abort()
			return
		}
		if perm != "" && !auth.Can(ctx, perm) {
			ownerGuardLog.Warn("owner action refused: route=%s http_method=%s user=%v why=%s",
				c.FullPath(), c.Request.Method,
				userID, "missing permission "+string(perm))
			httputil.RespondWithForbidden(c, "permission denied: "+string(perm))
			c.Abort()
			return
		}
		// The audit line for every owner action: who, proven how, did what.
		ownerGuardLog.Info("owner action allowed: route=%s http_method=%s user=%v access_email=%v method=%s query=%s",
			c.FullPath(), c.Request.Method,
			userID, auth.AccessEmailFromContext(ctx),
			string(auth.MethodFromContext(ctx)), c.Request.URL.RawQuery)
		c.Next()
	}
}
