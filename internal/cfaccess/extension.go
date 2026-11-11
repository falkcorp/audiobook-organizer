// file: internal/cfaccess/extension.go
// version: 1.0.1
// guid: 9381baf4-4f30-4810-b629-127b03ab2332
// last-edited: 2026-11-11

package cfaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"
	coreoidc "github.com/falkcorp/mediacore/auth/oidc"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// ExtensionName is the extension's name; its method is "ext:" + ExtensionName
// (05-authn 3.3).
const ExtensionName = "cfaccess"

// CookieName is the cookie Cloudflare Access sets next to the header. Browser
// flows may deliver only the cookie ("not guaranteed to be passed" in some
// setups, per Cloudflare).
const CookieName = "CF_Authorization"

// OrderCFAccess is the extension's place in stage 12: after the API-key
// authenticator (a key presented anywhere wins) and before the session
// authenticator, as the gin binder ran ahead of RequireAuth's session check.
const OrderCFAccess coreauth.Order = 150

// AccountResolver turns a verified identity into a local user under a policy.
// *oidc.Resolver is one.
type AccountResolver interface {
	Resolve(ctx context.Context, pol coreoidc.Policy, c coreoidc.Claims) (*coreauth.User, error)
}

// maxCookieTokens bounds how many CF_Authorization cookies one request may
// make the extension verify: a cookie header is attacker-sized, a verification
// is a signature check.
const maxCookieTokens = 5

// warnEvery is the least time between two "token did not verify" log lines. The
// line is the only trace of a bad token and a flood of them must not be a flood
// of log lines; the count of those held back rides on the next line.
const warnEvery = 30 * time.Second

// AssertionVerifier checks an Access JWT (signature, issuer, expiry, AUD) and
// says when it was issued. *CFAccessVerifier is one.
type AssertionVerifier interface {
	VerifyIssued(ctx context.Context, rawJWT string) (*VerifiedAssertion, error)
}

// ExtensionConfig is what the Cloudflare Access extension is built from.
type ExtensionConfig struct {
	// Verifier checks the Access JWT (signature, issuer, expiry, AUD).
	Verifier AssertionVerifier
	// Resolver admits the verified identity to a local user.
	Resolver AccountResolver
	// Policy is the admission and linking rule for an Access identity
	// (Provider must be "cfaccess").
	Policy coreoidc.Policy
	// OwnerEmail reports the configured owner e-mail, read on every request
	// so a change takes effect at once. "" means no owner is set.
	OwnerEmail func() string
	Log        *slog.Logger
	Clock      func() time.Time
}

// extension is the auth.Authenticator.
type extension struct {
	c ExtensionConfig

	warnMu   sync.Mutex
	warnAt   time.Time
	warnHeld int
}

// warn logs msg at most once per warnEvery, saying how many were held back.
func (e *extension) warn(msg string) {
	e.warnMu.Lock()
	now := e.c.Clock()
	if !e.warnAt.IsZero() && now.Sub(e.warnAt) < warnEvery {
		e.warnHeld++
		e.warnMu.Unlock()
		return
	}
	held := e.warnHeld
	e.warnAt, e.warnHeld = now, 0
	e.warnMu.Unlock()
	e.c.Log.Warn(msg, "suppressed", held)
}

// Extension returns Cloudflare Access as an identity extension for
// app.Options.Authenticators (F4-08 registers it): the verified Access JWT is
// the only thing that identifies a caller, and the owner assurance it proves
// is "the verified JWT's e-mail is the configured owner e-mail".
//
// Core ships no Cloudflare code; this is the whole of it behind
// auth.Extension. An invalid configuration is an error, not a pass-through:
// registering an extension that cannot verify anything would be a silent
// no-op the operator takes for protection.
func Extension(c ExtensionConfig) (coreauth.Extension, error) {
	switch {
	case c.Verifier == nil || c.Resolver == nil || c.OwnerEmail == nil:
		return coreauth.Extension{}, errors.New("cfaccess: an extension needs a Verifier, a Resolver and OwnerEmail")
	case c.Policy.Provider != "cfaccess":
		return coreauth.Extension{}, errors.New("cfaccess: the policy's provider must be cfaccess")
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	return coreauth.Extension{
		Authenticator: &extension{c: c},
		Spec: coreauth.MethodSpec{
			Method:        coreauth.Method("ext:" + ExtensionName),
			Interactive:   true,
			OwnerVerifier: true,
		},
		Order:    OrderCFAccess,
		OwnerSet: func() bool { return strings.TrimSpace(c.OwnerEmail()) != "" },
	}, nil
}

func (e *extension) Name() string { return ExtensionName }

// Authenticate answers one request:
//
//   - No Access JWT (neither the header nor a cookie): ErrNoCredential, the
//     next method decides.
//   - A header JWT that does not verify (signature, issuer, AUD, expiry): a
//     presented invalid credential, ErrInvalidCredentials. It never falls
//     through to a weaker method (05-authn 3.3). The header is what Cloudflare
//     injects at the edge, so a bad one is a forgery or a misrouted request.
//   - A cookie JWT that does not verify: ErrNoCredential. The cookie is the
//     browser's, and an expired one (Access cookies outlive the session) or one
//     planted by another site on a shared parent domain must not lock the
//     browser out of the session it also holds: nothing is granted for it and
//     the session authenticator runs. Every CF_Authorization cookie is tried,
//     so a planted one cannot hide the real one; the first that verifies wins.
//   - Cloudflare's keys cannot be fetched: ErrExtensionUnavailable. Nothing is
//     granted; stage 12 opens to the next method and counts the error.
//   - A valid token that names no person (a service token): ErrNoCredential;
//     it is a machine credential, never an identity, and the request goes on
//     to whatever else it carries (the ABS bearer).
//   - A verified identity this app does not admit: ErrNoCredential, as the
//     gin binder fell through to normal auth. An inactive or reset-required
//     account is refused with its own error; a store failure is returned as
//     is (stage 12 answers 503).
//   - Otherwise an Assertion for the resolved user, Owner set when the JWT's
//     e-mail is the configured owner and AuthTime the token's iat. The
//     Assertion carries no scopes and never raises the user's checker.
//
// Cf-Access-Authenticated-User-Email and every forwarding header are
// unsigned and are never read.
func (e *extension) Authenticate(r *http.Request) (*coreauth.Assertion, error) {
	if raw := strings.TrimSpace(r.Header.Get(CFAccessHeader)); raw != "" {
		return e.authenticate(r, raw, true)
	}
	var unavailable error
	tried := 0
	for _, ck := range r.Cookies() {
		raw := strings.TrimSpace(ck.Value)
		if ck.Name != CookieName || raw == "" {
			continue
		}
		if tried++; tried > maxCookieTokens {
			break
		}
		a, err := e.authenticate(r, raw, false)
		switch {
		case err == nil:
			return a, nil
		case errors.Is(err, coreauth.ErrNoCredential):
		case errors.Is(err, coreauth.ErrExtensionUnavailable):
			unavailable = err
		default:
			return nil, err // a verified identity's own refusal is its answer
		}
	}
	if unavailable != nil {
		return nil, unavailable
	}
	return nil, coreauth.ErrNoCredential
}

// authenticate judges one token. fromHeader says where it came from: a bad
// header is terminal, a bad cookie is no credential.
func (e *extension) authenticate(r *http.Request, raw string, fromHeader bool) (*coreauth.Assertion, error) {
	va, err := e.c.Verifier.VerifyIssued(r.Context(), raw)
	switch {
	case errors.Is(err, ErrKeysUnavailable):
		return nil, fmt.Errorf("%w: cloudflare access keys", coreauth.ErrExtensionUnavailable)
	case errors.Is(err, ErrNonIdentityAssertion):
		return nil, coreauth.ErrNoCredential
	case err != nil:
		// The class only: the library's text can quote the token's claims.
		e.warn("cfaccess: Access JWT presented but did not verify")
		if fromHeader {
			return nil, coreauth.ErrInvalidCredentials
		}
		return nil, coreauth.ErrNoCredential
	}
	claims := va.Claims
	if claims == nil || claims.Email == "" || claims.Subject == "" {
		return nil, coreauth.ErrNoCredential
	}
	user, err := e.c.Resolver.Resolve(r.Context(), e.c.Policy, coreoidc.Claims{
		Provider: e.c.Policy.Provider, Subject: claims.Subject, Email: claims.Email,
		EmailVerified: true, // Cloudflare mints the token only after its own IdP login
	})
	switch {
	case errors.Is(err, coreauth.ErrStoreUnavailable), errors.Is(err, coreauth.ErrInactiveUser), errors.Is(err, coreauth.ErrResetRequired):
		return nil, err
	case err != nil:
		e.warn("cfaccess: verified identity not admitted")
		return nil, coreauth.ErrNoCredential
	}
	authTime := va.IssuedAt.UTC()
	if now := e.c.Clock().UTC(); authTime.After(now) {
		authTime = now // an issue time from the future is not a longer-ago sign-in
	}
	return &coreauth.Assertion{
		User:         user.ID,
		CredentialID: "cfaccess:" + claims.Subject,
		AuthTime:     authTime,
		Owner:        auth.IsOwnerEmail(claims.Email, e.c.OwnerEmail()),
	}, nil
}
