// file: internal/oauth/resolve.go
// version: 1.1.0
// guid: 6b0d3f81-9a27-4c54-8e16-2a5c7b9e0d43
// last-edited: 2026-07-31

package oauth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Sentinel errors so callers can map to the right HTTP status (both → 403, never 500).
var (
	ErrEmailNotVerified = errors.New("oauth: identity email is not verified")
	ErrEmailNotAllowed  = errors.New("oauth: email is not on the allowlist")
)

// UserStore is the slice of database.Store the resolver needs. database.Store
// satisfies it directly.
type UserStore interface {
	GetOAuthIdentityByProviderSubject(provider, subject string) (*database.OAuthIdentity, error)
	CreateOAuthIdentity(identity *database.OAuthIdentity) (*database.OAuthIdentity, error)
	GetUserByEmail(email string) (*database.User, error)
	GetUserByUsername(username string) (*database.User, error)
	GetUserByID(id string) (*database.User, error)
	CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*database.User, error)
}

// ResolveUser turns a verified IdentityClaims into a local user, applying the security
// gates IN ORDER:
//  1. Reject if the IdP did not verify the email (never trust an unverified address).
//  2. Reject if the email is not on the allowlist — a valid IdP login by an
//     unauthorized account must NOT create a user or a session (verified ≠ authorized).
//  3. Resolve by (provider, subject) → existing linked user.
//  4. Else resolve by verified email → auto-link this identity to that user.
//  5. Else auto-create a user (DefaultRole) + link the identity.
//
// A rejected identity never touches the user/identity store past the lookups.
func (c *Config) ResolveUser(store UserStore, claims IdentityClaims) (*database.User, error) {
	if !claims.EmailVerified {
		return nil, ErrEmailNotVerified
	}
	if !c.IsEmailAllowed(claims.Email) {
		return nil, ErrEmailNotAllowed
	}

	// (3) Already linked by stable (provider, subject).
	if id, err := store.GetOAuthIdentityByProviderSubject(claims.Provider, claims.Subject); err != nil {
		return nil, fmt.Errorf("oauth resolve: lookup identity: %w", err)
	} else if id != nil {
		user, err := store.GetUserByID(id.UserID)
		if err != nil {
			return nil, fmt.Errorf("oauth resolve: load linked user: %w", err)
		}
		if user == nil {
			return nil, fmt.Errorf("oauth resolve: linked identity %s points to missing user %s", id.ID, id.UserID)
		}
		return user, nil
	}

	// (4) Link to an existing user with the same verified email.
	existing, err := store.GetUserByEmail(claims.Email)
	if err != nil {
		return nil, fmt.Errorf("oauth resolve: lookup user by email: %w", err)
	}
	if existing != nil {
		if _, err := store.CreateOAuthIdentity(&database.OAuthIdentity{
			Provider: claims.Provider, Subject: claims.Subject, Email: claims.Email, UserID: existing.ID,
		}); err != nil {
			return nil, fmt.Errorf("oauth resolve: link identity: %w", err)
		}
		return existing, nil
	}

	// (5) Auto-create a new user + link the identity.
	username := c.uniqueUsername(store, claims)
	user, err := store.CreateUser(username, claims.Email, "oauth", "", []string{c.DefaultRole}, "active")
	if err != nil {
		return nil, fmt.Errorf("oauth resolve: create user: %w", err)
	}
	if _, err := store.CreateOAuthIdentity(&database.OAuthIdentity{
		Provider: claims.Provider, Subject: claims.Subject, Email: claims.Email, UserID: user.ID,
	}); err != nil {
		return nil, fmt.Errorf("oauth resolve: link new identity: %w", err)
	}
	return user, nil
}

// uniqueUsername returns the verified email address itself as the username.
//
// The username IS the email, deliberately. A username derived from the local part
// ("johnathan.falk" out of johnathan.falk@gmail.com) is ambiguous the moment a second
// identity provider or a second domain is in play — two different people can own the
// same local part — and it leaves an SSO-provisioned account whose name matches nothing
// the owner ever typed. The verified email is already unique, already proven to belong
// to the person, and is what they actually sign in with.
//
// The numeric-suffix loop is kept only for the pathological case where some pre-existing
// local account has already claimed that exact username while NOT being reachable by the
// email lookup in step (4) above — e.g. a hand-made account whose email field is empty.
// Creating `user@example.com1` there is ugly, but silently binding a federated identity
// to an unrelated local account would be an account-takeover bug.
func (c *Config) uniqueUsername(store UserStore, claims IdentityClaims) string {
	base := sanitizeUsername(claims.Email)
	if base == "" {
		base = "user"
	}
	candidate := base
	for i := 1; i < 100; i++ {
		if u, err := store.GetUserByUsername(candidate); err == nil && u == nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s%d", base, i)
	}
	return candidate // extremely unlikely fall-through
}

// sanitizeUsername reduces an identity string to a conservative username charset.
//
// '@' and '+' are permitted so that a full email address survives intact — without '@'
// an address would silently collapse to "johnathan.falkgmail.com", which looks like a
// typo and is not the address anyone would type. Everything outside the allowlist is
// dropped rather than escaped: usernames are compared and displayed in a lot of places,
// and a restricted charset is the cheap way to keep them boring.
func sanitizeUsername(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' || r == '@' || r == '+' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
