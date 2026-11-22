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
	// Refresh re-resolves a principal held by a request that outlives its
	// permission check (an event stream), as core.auth Service.Refresh does:
	// it fails when the principal no longer stands (user disabled or removed,
	// session or key gone, epoch moved) and otherwise returns the principal
	// with a fresh checker, so a role lowered after the stream opened stops
	// being held. HEAD answered the same question from books' own store
	// (permHoldsLive); under core identity that store is no longer the
	// account's record, so the question goes to core.
	Refresh func(context.Context, *coreauth.Principal) (*coreauth.Principal, error)
	// Access resolves a verified Cloudflare Access person on the ABS surface
	// (single sign-on, Mode A, and the A7 app hand-off's hop 2) through core's
	// account resolver under the Access admission policy. Under core identity
	// the account record is core's: resolving a person through books' store
	// would create, link or mark rows core no longer owns.
	Access AccessResolver
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
	case id.Refresh == nil:
		return errors.New("legacyauth: identity has no principal refresh")
	case id.Access == nil:
		return errors.New("legacyauth: identity has no Cloudflare Access resolver")
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

// AdminRefused is the core-identity leg of a role-name admin test (F4-08 B7):
// it reports whether the request runs under core identity and the caller does
// not hold core:admin. A site keeps HEAD's role-name test exactly as it was and
// adds this as a further refusal, so under core identity the caller must pass
// both and the outcome can only narrow from HEAD's, never widen. With no
// Identity (LegacyAuthGin) it is false and HEAD alone decides.
func AdminRefused(ctx context.Context) bool {
	b, ok := BridgeFrom(ctx)
	return ok && !b.AdminHeld(ctx)
}

// KeyMayActAsAdmin is HEAD's APIKeyMayActAsAdmin under core identity (F4-08
// B7c), and bridged reports whether the request runs under core identity at
// all (false: HEAD's own test decides, may is meaningless). A principal that is
// not an API key may use the admin role, as a request with no key did. An API
// key may only when it holds every legacy atom, judged through the checker
// stage 12 bound: HEAD refused a key whose non-empty scopes leave out an atom
// and passed a key with empty scopes, which holds all of its owner's
// permissions, and an admin owner holds every atom, so the two agree. A
// missing principal fails closed.
func KeyMayActAsAdmin(ctx context.Context) (may, bridged bool) {
	b, ok := BridgeFrom(ctx)
	if !ok {
		return false, false
	}
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok {
		return false, true
	}
	if p.Method != coreauth.MethodAPIKey {
		return true, true
	}
	return b.keyHoldsEveryAtom(ctx), true
}

// keyHoldsEveryAtom reports whether the caller satisfies the table entry of
// every atom internal/auth defines (auth.All). An atom the table does not know
// is not held.
func (b *Bridge) keyHoldsEveryAtom(ctx context.Context) bool {
	for _, p := range auth.All() {
		if !b.Can(ctx, string(p)) {
			return false
		}
	}
	return true
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

// Held is the in-handler check of an adopted site under core identity: whether
// the caller satisfies the atom's table entry (Bridge.Can), and whether the
// request runs under core identity at all. bridged false means the server was
// built without an Identity (LegacyAuthGin) and the caller answers with HEAD's
// own code; held is then meaningless. The auth-disabled principal does not
// hold any atom (Check), so a site whose HEAD code bypasses its check with
// auth off asks HeldOrAuthOff.
func Held(ctx context.Context, atom string) (held, bridged bool) {
	b, ok := BridgeFrom(ctx)
	if !ok {
		return false, false
	}
	return b.Can(ctx, atom), true
}

// HeldOrAuthOff is Held for a site whose HEAD code holds every permission when
// auth is off (middleware.RequirePermission's s.perm, the ops v3 Holds): the
// auth-disabled principal passes and any other caller is judged as Held does.
// HEAD read books' EnableAuth for this; core's reading is the principal.
func HeldOrAuthOff(ctx context.Context, atom string) (held, bridged bool) {
	b, ok := BridgeFrom(ctx)
	if !ok {
		return false, false
	}
	return b.AuthOff(ctx) || b.Can(ctx, atom), true
}

// HeldLive is HeldOrAuthOff for a request that outlives its permission check:
// the principal is first re-resolved through the identity's Refresh, so an
// account that was disabled, removed or demoted after the request began stops
// holding what it lost (HEAD's permHoldsLive recheck against books' store). A
// refusal, a nil Refresh or a refreshed principal without a checker fails
// closed. The auth-disabled principal is passed to Refresh like any other:
// core answers it afresh while the mode is on and refuses it once it is off.
// Refresh can only narrow what the stream was opened with, never widen it.
func HeldLive(ctx context.Context, atom string) (held, bridged bool) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return false, false
	}
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok || id.Refresh == nil {
		return false, true
	}
	np, err := id.Refresh(ctx, p)
	if err != nil || np == nil || np.Perms == nil {
		return false, true
	}
	fresh := rbac.WithChecker(coreauth.WithPrincipal(ctx, np), np.Perms)
	return id.Bridge.AuthOff(fresh) || id.Bridge.Can(fresh, atom), true
}
