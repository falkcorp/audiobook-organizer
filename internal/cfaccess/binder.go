// file: internal/cfaccess/binder.go
// version: 1.8.2
// guid: 8d1a4f92-3c07-4b56-9e28-6a0b5c2e7d41
// last-edited: 2026-11-11

package cfaccess

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
)

// cfAccessVerifiedKey is the gin context key CloudflareAccessAuth sets once a
// request's Cf-Access-Jwt-Assertion has been verified (signature, issuer, expiry
// and AUD). Unexported on purpose: only a verified assertion can set it, so
// reading it via CFAccessVerified is proof the request passed Cloudflare Access
// (or service-token) verification at the edge.
const cfAccessVerifiedKey = "cf_access_verified"

// CFAccessVerified reports whether CloudflareAccessAuth verified this
// request's Access assertion. It is true for a human login AND for a service
// token (a verified assertion that names no person), whether or not the
// identity was admitted to a local user; it is false when Access is not
// configured, no assertion was presented, or the assertion did not verify.
// Callers use it to decide whether Cloudflare-set request headers such as
// CF-Connecting-IP can be believed.
func CFAccessVerified(c *gin.Context) bool {
	v, _ := c.Get(cfAccessVerifiedKey)
	ok, _ := v.(bool)
	return ok
}

// MarkCFAccessVerified records that c's Access assertion verified. Exported
// only so tests in other packages can stand in for CloudflareAccessAuth;
// production code reaches it solely through that middleware.
func MarkCFAccessVerified(c *gin.Context) { c.Set(cfAccessVerifiedKey, true) }

// CFAccessAuthenticator resolves a Cloudflare-Access-authenticated request to a local
// user: it verifies the signed Access JWT, applies the same allowlist gate as the
// OAuth login, and (idempotently) links/creates the user. Constructed only when
// Cloudflare Access is configured; otherwise the middleware is a pass-through.
type CFAccessAuthenticator struct {
	// verifier is the CFAssertionVerifier the ABS surface also takes, so a
	// test can drive the middleware without Cloudflare's keys. nil (never a
	// typed nil: NewCFAccessAuthenticator maps a nil pointer to nil) makes
	// the middleware a pass-through.
	verifier CFAssertionVerifier
	cfg      *legacyoauth.Config
	store    legacyoauth.UserStore
	hooks    Hooks
	warns    warnGate // one failure line per warnEvery: each bad request would otherwise log one
}

// warn logs reason (a class, never a token claim or an address) at most once per
// warnEvery, with the number of lines held back.
func (a *CFAccessAuthenticator) warn(reason string) {
	if ok, held := a.warns.allow(time.Now()); ok {
		cfBounceLog.Warn("cfaccess: %s (suppressed=%d)", reason, held)
	}
}

// Hooks is what the gin binder takes from the gin auth chain it feeds, so this
// package never imports internal/server/middleware (which imports it). The
// middleware package builds them (CFAccessHooks); every field is required, and
// the binder is a pass-through while one is missing. F4-08 deletes the binder
// with the gin chain.
type Hooks struct {
	// KeyPresented reports whether r presents an API key. A request that does
	// is authenticated by the key, never by an Access assertion riding along
	// with it.
	KeyPresented func(r *http.Request) bool
	// BindUser records user in the gin context as the request's authenticated
	// user. That context key is load-bearing for RequireAuth (it skips its own
	// session check when it is set), so it is the chain's to set.
	BindUser func(c *gin.Context, user *database.User)
	// Permissions is the union of user's role permissions.
	Permissions func(user *database.User) []auth.Permission
}

func (h Hooks) complete() bool {
	return h.KeyPresented != nil && h.BindUser != nil && h.Permissions != nil
}

// NewCFAccessAuthenticator wires the verifier, config, and store. Any nil → the
// middleware becomes a no-op pass-through.
func NewCFAccessAuthenticator(verifier *CFAccessVerifier, cfg *legacyoauth.Config, store legacyoauth.UserStore, hooks Hooks) *CFAccessAuthenticator {
	a := &CFAccessAuthenticator{cfg: cfg, store: store, hooks: hooks}
	if verifier != nil {
		a.verifier = verifier
	}
	return a
}

// NewCFAccessAuthenticatorWithVerifier is NewCFAccessAuthenticator for any
// CFAssertionVerifier (a test's fake). A nil verifier makes the middleware a
// pass-through.
func NewCFAccessAuthenticatorWithVerifier(verifier CFAssertionVerifier, cfg *legacyoauth.Config, store legacyoauth.UserStore, hooks Hooks) *CFAccessAuthenticator {
	return &CFAccessAuthenticator{verifier: verifier, cfg: cfg, store: store, hooks: hooks}
}

// CloudflareAccessAuth returns a middleware that, when a valid Cf-Access-Jwt-Assertion
// is present, binds the resolved user into the request so the downstream RequireAuth
// skips its session check. It is FAIL-OPEN to the normal auth path: a missing header,
// an invalid JWT, or a non-allowlisted identity simply falls through (RequireAuth then
// enforces session/API-key auth and returns 401 if there is none). The JWT — not the
// spoofable Cf-Access-Authenticated-User-Email header — is the trust anchor.
func CloudflareAccessAuth(a *CFAccessAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if a == nil || a.verifier == nil || a.cfg == nil || a.store == nil || !a.hooks.complete() {
			c.Next()
			return
		}
		// A request that presents an API key is authenticated by the key
		// (RequireAuth), never by an Access assertion riding along with it.
		if a.hooks.KeyPresented(c.Request) {
			c.Next()
			return
		}
		// Prefer the Cf-Access-Jwt-Assertion header; fall back to the CF_Authorization
		// cookie (browser flows may deliver only the cookie — Cloudflare says the header
		// is "not guaranteed to be passed" in some setups).
		raw := c.GetHeader(CFAccessHeader)
		if raw == "" {
			if ck, err := c.Cookie("CF_Authorization"); err == nil {
				raw = ck
			}
		}
		if raw == "" {
			c.Next()
			return
		}
		claims, err := a.verifier.Verify(c.Request.Context(), raw)
		// Verify reports a service-token assertion (valid signature, issuer,
		// expiry and AUD, but no email claim) as ErrNonIdentityAssertion. That
		// token is still proof the request passed Cloudflare Access, so it
		// sets the verified marker; it just resolves no user.
		if err == nil || errors.Is(err, ErrNonIdentityAssertion) {
			MarkCFAccessVerified(c)
		}
		if err != nil {
			// Diagnostic: a JWT was present but did not verify (aud/issuer/keyset/expiry).
			// Rate limited, class only (the library's text can quote the token's
			// claims); falls through to normal auth.
			if errors.Is(err, ErrKeysUnavailable) {
				a.warn("Access JWT present but the signing keys are unavailable")
			} else {
				a.warn("Access JWT present but verification failed")
			}
			c.Next()
			return
		}
		user, err := a.cfg.ResolveUser(a.store, *claims)
		if err != nil {
			a.warn("verified identity not admitted")
			c.Next() // not allowlisted / not verified — normal auth will 401
			return
		}
		a.hooks.BindUser(c, user)
		perms := a.hooks.Permissions(user)
		ctx := auth.WithUser(c.Request.Context(), user)
		ctx = auth.WithPermissions(ctx, perms)
		ctx = auth.WithMethod(ctx, auth.MethodCFAccess)
		// The verified JWT's email, for the owner check (auth.OwnerProofWhyNot).
		// Never the unsigned Cf-Access-Authenticated-User-Email header.
		ctx = auth.WithAccessEmail(ctx, claims.Email)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
