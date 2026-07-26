// file: internal/server/middleware/cfaccess.go
// version: 1.0.0
// guid: 8d1a4f92-3c07-4b56-9e28-6a0b5c2e7d41
// last-edited: 2026-07-26

package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// CFAccessAuthenticator resolves a Cloudflare-Access-authenticated request to a local
// user: it verifies the signed Access JWT, applies the same allowlist gate as the
// OAuth login, and (idempotently) links/creates the user. Constructed only when
// Cloudflare Access is configured; otherwise the middleware is a pass-through.
type CFAccessAuthenticator struct {
	verifier *oauth.CFAccessVerifier
	cfg      *oauth.Config
	store    database.Store
}

// NewCFAccessAuthenticator wires the verifier, config, and store. Any nil → the
// middleware becomes a no-op pass-through.
func NewCFAccessAuthenticator(verifier *oauth.CFAccessVerifier, cfg *oauth.Config, store database.Store) *CFAccessAuthenticator {
	return &CFAccessAuthenticator{verifier: verifier, cfg: cfg, store: store}
}

// CloudflareAccessAuth returns a middleware that, when a valid Cf-Access-Jwt-Assertion
// is present, binds the resolved user into the request so the downstream RequireAuth
// skips its session check. It is FAIL-OPEN to the normal auth path: a missing header,
// an invalid JWT, or a non-allowlisted identity simply falls through (RequireAuth then
// enforces session/API-key auth and returns 401 if there is none). The JWT — not the
// spoofable Cf-Access-Authenticated-User-Email header — is the trust anchor.
func CloudflareAccessAuth(a *CFAccessAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if a == nil || a.verifier == nil || a.cfg == nil || a.store == nil {
			c.Next()
			return
		}
		raw := c.GetHeader(oauth.CFAccessHeader)
		if raw == "" {
			c.Next()
			return
		}
		claims, err := a.verifier.Verify(c.Request.Context(), raw)
		if err != nil {
			c.Next() // not a valid Access token — let normal auth handle it
			return
		}
		user, err := a.cfg.ResolveUser(a.store, *claims)
		if err != nil {
			c.Next() // not allowlisted / not verified — normal auth will 401
			return
		}
		c.Set(contextUserKey, user)
		perms := effectivePermissionsFor(a.store, user)
		ctx := auth.WithUser(c.Request.Context(), user)
		ctx = auth.WithPermissions(ctx, perms)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
