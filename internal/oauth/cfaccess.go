// file: internal/oauth/cfaccess.go
// version: 1.0.0
// guid: 3a7e0c92-8b41-4d56-9f08-1c6b2a5d7e39
// last-edited: 2026-07-26

package oauth

import (
	"context"
	"fmt"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

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
		return nil, fmt.Errorf("cfaccess: jwt has no email claim")
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
