// file: internal/oauth/cfaccess.go
// version: 1.1.0
// guid: 3a7e0c92-8b41-4d56-9f08-1c6b2a5d7e39
// last-edited: 2026-08-01

package oauth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

// ErrNonIdentityAssertion reports an Access token that is cryptographically VALID —
// correct signature, issuer, expiry and aud — but carries no identity, i.e. no email
// claim. Cloudflare mints exactly this shape for a service token, where the caller is
// a machine credential rather than a person.
//
// It is a distinct sentinel because "no identity in this token" and "this token is
// not trustworthy" demand opposite handling. A forged or expired token must be a
// terminal 401: the credential is bad and no other credential should rescue it. A
// non-identity token is merely silent about who the caller is, so the request should
// fall through to whatever other credential it carries — for us, the ABS bearer token
// — instead of being rejected outright. Collapsing the two (the original bug) made a
// service token fatal even when the request also presented a perfectly valid bearer,
// which is precisely the Mode B topology the edge is configured for.
//
// Callers MUST match with errors.Is, not string comparison; it is returned wrapped.
var ErrNonIdentityAssertion = errors.New("cfaccess: assertion carries no identity (no email claim)")

// CFAccessHeader is the request header Cloudflare Access injects with the signed
// application token once a user has authenticated at the edge.
const CFAccessHeader = "Cf-Access-Jwt-Assertion"

// CFAccessVerifier verifies a Cloudflare Access application JWT against the team's
// public JWKS and the expected AUD (Access application audience) tag. The JWT — NOT
// the convenience Cf-Access-Authenticated-User-Email header — is the trust anchor:
// the header is unsigned and spoofable to a direct-to-origin request.
type CFAccessVerifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewCFAccessVerifier builds a verifier bound to the team domain (e.g.
// "myteam.cloudflareaccess.com") and the Access application AUD tag. It uses a remote
// keyset that refreshes automatically as Cloudflare rotates signing keys.
func NewCFAccessVerifier(ctx context.Context, teamDomain, aud string) (*CFAccessVerifier, error) {
	teamDomain = strings.TrimSpace(teamDomain)
	aud = strings.TrimSpace(aud)
	if teamDomain == "" || aud == "" {
		return nil, fmt.Errorf("cfaccess: team domain and AUD are required")
	}
	issuer := "https://" + teamDomain
	certsURL := issuer + "/cdn-cgi/access/certs"
	keySet := oidc.NewRemoteKeySet(ctx, certsURL)
	verifier := oidc.NewVerifier(issuer, keySet, &oidc.Config{ClientID: aud}) // ClientID = expected aud
	return &CFAccessVerifier{verifier: verifier}, nil
}

// Verify checks the signature, issuer, expiry, and aud of a Cloudflare Access JWT and
// returns the verified identity. Cloudflare only mints this token after its own IdP
// login, so the email is verified.
func (v *CFAccessVerifier) Verify(ctx context.Context, rawJWT string) (*IdentityClaims, error) {
	tok, err := v.verifier.Verify(ctx, rawJWT)
	if err != nil {
		return nil, fmt.Errorf("cfaccess: verify jwt: %w", err)
	}
	var claims struct {
		Email string `json:"email"`
		Sub   string `json:"sub"`
	}
	if err := tok.Claims(&claims); err != nil {
		return nil, fmt.Errorf("cfaccess: parse claims: %w", err)
	}
	if claims.Email == "" {
		// Wrapped, not returned bare, so the sub is available for logging while
		// errors.Is(err, ErrNonIdentityAssertion) still matches. Reaching here means
		// the signature/issuer/aud checks above all PASSED — this is a trusted token
		// that simply names no person.
		return nil, fmt.Errorf("%w (sub=%q)", ErrNonIdentityAssertion, claims.Sub)
	}
	subject := claims.Sub
	if subject == "" {
		subject = claims.Email // fall back to email as the stable id
	}
	return &IdentityClaims{
		Provider:      ProviderCFAccess,
		Subject:       subject,
		Email:         claims.Email,
		EmailVerified: true,
		Name:          "",
	}, nil
}
