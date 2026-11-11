// file: core/auth/oidc/oidc.go
// version: 1.0.0
// guid: 20d8f08a-07b7-4234-9b8f-7fdbfeb0cf53
// last-edited: 2026-11-11

// Package oidc is core's generic OIDC and OAuth2 sign-in (05-authn §4.7):
// providers configured as a list (an `oidc` kind discovered from its issuer, a
// `github` kind for GitHub's non-OIDC OAuth2), PKCE S256 for every provider, a
// signed single-use state that also carries the native-app hand-off, linking
// by (provider, subject), and admission by allowlist and invite grant. It
// ends in an auth.Service session, or, for a sign-in a native app started, in
// the hand-off the app's module registered (auth.MethodSpec.AppHandoff).
//
// It holds no routes: F4-05 serves them over Flow. It is dark until then.
//
// # Security model
//
//   - A provider's claims are trusted only after the provider proved them: an
//     OIDC id_token is verified for signature, issuer, audience and expiry and
//     must echo the nonce this flow sent; GitHub's address is the primary (or
//     any) VERIFIED one. An unverified address never signs anyone in.
//   - Verified is not authorized. An identity not on the provider's allowlist
//     and not invited creates nothing.
//   - An unseen subject never takes an existing account by e-mail unless the
//     provider is trusted for it (link_existing_by_email), the account's
//     address is one we verified, and the account holds no core:admin and has
//     no password (SEC-7). Otherwise the sign-in is refused (email_in_use).
//   - The redirect URI is built from configuration, never from the request. A
//     post-login return path must be a same-site absolute path. A native app's
//     redirect_uri is accepted only when the module's hand-off allows it
//     exactly, and the location the hand-off returns must stay on it.
//   - State is signed (HMAC-SHA256, a key derived from the secrets store),
//     bound to the browser by a cookie the caller sets, bound to its provider,
//     valid for ten minutes and spent by its first use.
//   - No token, code, secret or id_token reaches a log or an error text.
package oidc

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/falkcorp/mediacore/rbac"
)

// Provider kinds (05-authn §8).
const (
	KindOIDC   = "oidc"
	KindGitHub = "github"
)

// Claims is the normalized identity a provider vouches for after a successful
// code exchange. Subject is the provider's stable account id: linking is by
// (Provider, Subject), never by e-mail.
type Claims struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// ProviderConfig is one entry of core.auth.oidc.providers[] (05-authn §8)
// with its client secret resolved from core.auth.oidc.<id>.client_secret.
type ProviderConfig struct {
	ID       string
	Kind     string
	Issuer   string // KindOIDC: the issuer URL discovery runs against
	ClientID string
	// ClientSecret is the resolved secret. It is never logged, never in an
	// error, and not in this type's String, GoString or LogValue.
	ClientSecret   string
	Scopes         []string
	AllowedEmails  []string
	AllowedDomains []string
	DefaultRole    rbac.RoleName
	// LinkExistingByEmail trusts this provider to speak for the owner of an
	// address: an unseen subject may then take an existing account by e-mail,
	// under the guard of §4.7 (default false).
	LinkExistingByEmail bool
}

// validProviderID reports whether s is [a-z][a-z0-9_]{0,47}.
func validProviderID(s string) bool {
	if s == "" || len(s) > 48 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// Validate reports why c cannot be built, without its secret in the text.
func (c ProviderConfig) Validate() error {
	switch {
	case !validProviderID(c.ID):
		return fmt.Errorf("oidc: provider id %q is not [a-z][a-z0-9_]{0,47}", c.ID)
	case c.ID == "providers" || c.ID == "redirect_base_url":
		return fmt.Errorf("oidc: provider id %q is a reserved key name", c.ID)
	case c.Kind != KindOIDC && c.Kind != KindGitHub:
		return fmt.Errorf("oidc: provider %s: kind %q is neither %s nor %s", c.ID, c.Kind, KindOIDC, KindGitHub)
	case c.ClientID == "":
		return fmt.Errorf("oidc: provider %s: client_id is required", c.ID)
	case c.ClientSecret == "":
		return fmt.Errorf("oidc: provider %s: no client secret", c.ID)
	}
	if c.Kind == KindOIDC {
		u, err := url.Parse(c.Issuer)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("oidc: provider %s: issuer must be an https URL without userinfo, query or fragment", c.ID)
		}
	}
	return nil
}

// Policy is the part of a provider's configuration admission and linking
// read. Another identity source (an extension, ABO's Cloudflare Access) uses
// a Policy of its own with Resolver.Resolve.
type Policy struct {
	Provider            string
	AllowedEmails       []string
	AllowedDomains      []string
	DefaultRole         rbac.RoleName
	LinkExistingByEmail bool
}

// Policy is c's admission and linking rules.
func (c ProviderConfig) Policy() Policy {
	return Policy{
		Provider: c.ID, AllowedEmails: c.AllowedEmails, AllowedDomains: c.AllowedDomains,
		DefaultRole: c.DefaultRole, LinkExistingByEmail: c.LinkExistingByEmail,
	}
}

// String, GoString and LogValue keep the client secret out of any rendering
// of a ProviderConfig (a %v in a log line, a slog attribute, a test dump).
func (c ProviderConfig) String() string   { return "oidc.ProviderConfig{" + c.ID + ", " + c.Kind + "}" }
func (c ProviderConfig) GoString() string { return c.String() }
func (c ProviderConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", c.ID), slog.String("kind", c.Kind))
}

// Settings is core.auth.oidc.* as one snapshot.
type Settings struct {
	// RedirectBaseURL is core.auth.oidc.redirect_base_url: the public origin
	// the provider calls back on. https, or http on a loopback host.
	RedirectBaseURL string
	Providers       []ProviderConfig
}

// validBase reports why a redirect base URL cannot be used.
func validBase(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("oidc: redirect_base_url must be an absolute URL without userinfo, query or fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if h := u.Hostname(); h == "localhost" || h == "127.0.0.1" || h == "::1" {
			return nil
		}
	}
	return errors.New("oidc: redirect_base_url must be https (http only on a loopback host)")
}
