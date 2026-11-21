// file: internal/server/legacyauth/adopt.go
// version: 0.1.0
// guid: b4c71e93-2d58-4a60-9e1f-6a3b8d0c5e27
// last-edited: 2026-11-21

package legacyauth

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// Adoption (F4-08 B7). The bridge's adapters replace HEAD's gin auth chain
// only on a server that was built to run under core identity
// (LegacyAuthCore): server.WithIdentity hands NewServer an Identity, NewServer
// binds it into every request's context (Identity.Bind), and each adopted call
// site asks BridgeFrom. The mode therefore comes from how the server was
// wired, never from what a request happens to carry: under LegacyAuthGin
// nothing binds an Identity, a core principal in the context changes nothing,
// and every site takes HEAD's path byte for byte. A request that reaches a
// bridged server without a principal is refused (never handed to HEAD's
// first-run bypass, which reads books' own user count).

// Identity is the wiring of a gin mount that runs under core identity.
type Identity struct {
	// Bridge is the adapters' value (New).
	Bridge *Bridge
	// Compat is books' method-bound auth handle (module.Deps.AuthCompat),
	// which the ABS surface authenticates through.
	Compat coreauth.Compat
	// Client reads a request's caller the way the kernel's resolver does
	// (address, user agent, proxy trust). Core's throttle keys on it, so the
	// handlers never build one from gin's own client address.
	Client func(*http.Request) coreauth.ClientInfo
}

// Validate reports a missing member. A server built over a partial Identity
// would run some sites under core and others under HEAD, so NewServer refuses
// it.
func (id *Identity) Validate() error {
	switch {
	case id == nil:
		return errors.New("legacyauth: no identity")
	case id.Bridge == nil:
		return errors.New("legacyauth: identity has no bridge")
	case id.Compat == nil:
		return errors.New("legacyauth: identity has no compat handle")
	case id.Client == nil:
		return errors.New("legacyauth: identity has no client resolver")
	}
	return nil
}

type identityKey struct{}

// WithIdentity returns ctx carrying id: the request runs under core identity.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom is the Identity of a request that runs under core identity.
// ok is false for a request on a server built without one (LegacyAuthGin),
// which every adopted site answers with HEAD's own code.
func IdentityFrom(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(*Identity)
	return id, ok && id != nil
}

// BridgeFrom is IdentityFrom's bridge.
func BridgeFrom(ctx context.Context) (*Bridge, bool) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return nil, false
	}
	return id.Bridge, true
}

// Bind is the middleware NewServer installs first on a bridged server: it
// marks every request as running under core identity.
func (id *Identity) Bind() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(WithIdentity(c.Request.Context(), id))
		c.Next()
	}
}

// AuthOff reports whether ctx's principal is the synthetic principal of
// core.auth.mode=disabled (05-authn §4.11). It is the core-identity reading of
// what HEAD's sites asked of books' own EnableAuth: a site that bypasses its
// check with auth off bypasses it for this principal and no other, and never
// reads books' config.
func (b *Bridge) AuthOff(ctx context.Context) bool {
	p, ok := coreauth.PrincipalFrom(ctx)
	return ok && authDisabled(p)
}

// AdminHeld reports whether the caller holds core:admin through the checker
// stage 12 bound (05-rbac §4.6): the permission that replaces every
// role-name admin test, with key scoping applied. A missing principal or
// checker, and the auth-disabled principal (core:admin is AuthRequired), do
// not hold it. A refusal is audited through the service stage 12 bound.
func (b *Bridge) AdminHeld(ctx context.Context) bool {
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok || authDisabled(p) {
		return false
	}
	return rbac.Require(ctx, rbac.CoreAdmin) == nil
}

// Authenticate is the adapter of HEAD's RequireAuth (05-http-api §8.3
// RequireAuth row). It authenticates nothing: it fills HEAD's context values
// from the principal stage 12 bound (FromCorePrincipal) and hands the user to
// setUser, which binds it as the gin context's user (middleware.CurrentUser).
// No principal, or one that cannot be mapped, is 401. The auth-disabled
// principal binds no user, as with auth off at HEAD, and passes: every later
// check judges it by its own checker.
func (b *Bridge) Authenticate(setUser func(*gin.Context, *database.User)) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		p, ok := coreauth.PrincipalFrom(ctx)
		if !ok {
			unauthenticated(c)
			return
		}
		if authDisabled(p) {
			c.Next()
			return
		}
		ctx, err := b.FromCorePrincipal(ctx)
		if err != nil {
			unauthenticated(c)
			return
		}
		if u, ok := auth.UserFromContext(ctx); ok && u != nil && setUser != nil {
			setUser(c, u)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
