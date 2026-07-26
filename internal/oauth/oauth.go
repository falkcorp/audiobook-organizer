// file: internal/oauth/oauth.go
// version: 1.0.0
// guid: 7c2a9e14-6b38-4d05-8f71-3a0e5b9d2c46
// last-edited: 2026-07-26

// Package oauth implements OAuth2 / OIDC single sign-on (GitHub, Google) and
// Cloudflare Access identity verification for the audiobook organizer. It is the
// PURE core: config, the CSRF-state + PKCE machinery, provider code-exchange, and
// JWT verification. It has NO gin/server dependency — the HTTP handlers live in the
// server layer and call into this package.
//
// SECURITY MODEL (do not weaken):
//   - Every login path (GitHub, Google, Cloudflare Access) yields an IdentityClaims
//     with a VERIFIED email. "Verified" means the IdP asserted the address belongs to
//     the account (GitHub primary+verified email, Google email_verified, a
//     Cloudflare-signed JWT).
//   - Verified is NOT the same as authorized. A valid IdP login by an account that is
//     not on the configured allowlist MUST be rejected — the server is not a public
//     app, so anyone with a Google/GitHub account could otherwise obtain a valid
//     identity. IsEmailAllowed is the gate, checked before any user/session is created.
package oauth

import (
	"fmt"
	"strings"
)

// Provider names (stable — persisted on OAuthIdentity.Provider and used in routes).
const (
	ProviderGitHub   = "github"
	ProviderGoogle   = "google"
	ProviderCFAccess = "cfaccess"
)

// IdentityClaims is the normalized identity a provider returns after a successful
// code exchange (or a verified Cloudflare Access JWT). Subject is the provider's
// STABLE account id; matching/linking is always by (provider, subject), never email.
type IdentityClaims struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// Config holds the resolved OAuth settings. Build it from config.AppConfig via
// FromAppConfig in the server layer. Zero value = everything disabled.
type Config struct {
	Enabled            bool
	GitHubClientID     string
	GitHubClientSecret string
	GoogleClientID     string
	GoogleClientSecret string
	RedirectBaseURL    string   // public origin, e.g. https://books.example.com
	AllowedEmails      []string // lowercased allowlist
	DefaultRole        string   // role for a newly auto-created OAuth user
	CFAccessTeamDomain string   // e.g. myteam.cloudflareaccess.com
	CFAccessAUD        string   // Access application AUD tag
}

// New normalizes an incoming config (lowercases the allowlist, trims blanks).
func New(c Config) *Config {
	allowed := make([]string, 0, len(c.AllowedEmails))
	for _, e := range c.AllowedEmails {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" {
			allowed = append(allowed, e)
		}
	}
	c.AllowedEmails = allowed
	c.RedirectBaseURL = strings.TrimRight(strings.TrimSpace(c.RedirectBaseURL), "/")
	if c.DefaultRole == "" {
		c.DefaultRole = "viewer"
	}
	return &c
}

// ParseAllowedEmails splits a comma-separated allowlist string into entries.
func ParseAllowedEmails(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IsEmailAllowed reports whether a verified email may log in. Case-insensitive exact
// match against the allowlist. An empty allowlist denies everyone (fail-closed).
func (c *Config) IsEmailAllowed(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, a := range c.AllowedEmails {
		if a == email {
			return true
		}
	}
	return false
}

// ProviderEnabled reports whether a given provider is configured.
func (c *Config) ProviderEnabled(provider string) bool {
	switch provider {
	case ProviderGitHub:
		return c.Enabled && c.GitHubClientID != "" && c.GitHubClientSecret != ""
	case ProviderGoogle:
		return c.Enabled && c.GoogleClientID != "" && c.GoogleClientSecret != ""
	default:
		return false
	}
}

// RedirectURI builds the provider callback URL from the configured base origin.
func (c *Config) RedirectURI(provider string) (string, error) {
	if c.RedirectBaseURL == "" {
		return "", fmt.Errorf("oauth: redirect base URL not configured")
	}
	return c.RedirectBaseURL + "/api/v1/auth/oauth/" + provider + "/callback", nil
}
