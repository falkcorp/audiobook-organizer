// file: internal/server/middleware/cfaccess.go
// version: 1.7.0
// guid: 8d1a4f92-3c07-4b56-9e28-6a0b5c2e7d41
// last-edited: 2026-10-19

package middleware

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
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
	cfg      *oauth.Config
	store    cfAccessStore
}

// NewCFAccessAuthenticator wires the verifier, config, and store. Any nil → the
// middleware becomes a no-op pass-through.
func NewCFAccessAuthenticator(verifier *oauth.CFAccessVerifier, cfg *oauth.Config, store cfAccessStore) *CFAccessAuthenticator {
	a := &CFAccessAuthenticator{cfg: cfg, store: store}
	if verifier != nil {
		a.verifier = verifier
	}
	return a
}

// NewCFAccessAuthenticatorWithVerifier is NewCFAccessAuthenticator for any
// CFAssertionVerifier (a test's fake). A nil verifier makes the middleware a
// pass-through.
func NewCFAccessAuthenticatorWithVerifier(verifier CFAssertionVerifier, cfg *oauth.Config, store cfAccessStore) *CFAccessAuthenticator {
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
		// A request that presents an API key is authenticated by the key
		// (RequireAuth), never by an Access assertion riding along with it.
		if APIKeyFromRequest(c.Request) != "" {
			c.Next()
			return
		}
		// Prefer the Cf-Access-Jwt-Assertion header; fall back to the CF_Authorization
		// cookie (browser flows may deliver only the cookie — Cloudflare says the header
		// is "not guaranteed to be passed" in some setups).
		raw := c.GetHeader(oauth.CFAccessHeader)
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
		if err == nil || errors.Is(err, oauth.ErrNonIdentityAssertion) {
			MarkCFAccessVerified(c)
		}
		if err != nil {
			// Diagnostic: a JWT was present but did not verify (aud/issuer/keyset/expiry).
			// Rate-limited-ish by being Warn; falls through to normal auth.
			slog.Warn("cfaccess: Access JWT present but verification failed", "err", err)
			c.Next()
			return
		}
		user, err := a.cfg.ResolveUser(a.store, *claims)
		if err != nil {
			slog.Warn("cfaccess: verified identity not admitted", "email", claims.Email, "err", err)
			c.Next() // not allowlisted / not verified — normal auth will 401
			return
		}
		c.Set(contextUserKey, user)
		perms := EffectivePermissionsFor(a.store, user)
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

// cfAccessStore is what this authenticator forwards its store into: the
// identity resolution in oauth.Config.ResolveUser, and the role lookup behind
// EffectivePermissionsFor. Both already declare narrow interfaces, so both are
// embedded BY NAME -- two entries, and this re-narrows on its own if either
// does. It was database.Store -- 398 methods -- until 2026-08-19.
type cfAccessStore interface {
	oauth.UserStore
	roleResolver
}
