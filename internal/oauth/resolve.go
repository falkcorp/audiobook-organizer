// file: internal/oauth/resolve.go
// version: 1.5.0
// guid: 6b0d3f81-9a27-4c54-8e16-2a5c7b9e0d43
// last-edited: 2026-10-20

package oauth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Sentinel errors so callers can map to the right HTTP status (both → 403, never 500).
var (
	ErrEmailNotVerified = errors.New("oauth: identity email is not verified")
	ErrEmailNotAllowed  = errors.New("oauth: email is not on the allowlist")
	// ErrEmailConflict: another account has this address as its VERIFIED
	// email, yet the login did not link to it (a concurrent change). Denied,
	// through the same ErrEmailNotAllowed path.
	ErrEmailConflict = fmt.Errorf("%w: another account has this email verified", ErrEmailNotAllowed)
)

var resolveLog = logger.New("oauth.resolve")

// UserStore is the slice of database.Store the resolver needs. database.Store
// satisfies it directly.
type UserStore interface {
	GetOAuthIdentityByProviderSubject(provider, subject string) (*database.OAuthIdentity, error)
	CreateOAuthIdentity(identity *database.OAuthIdentity) (*database.OAuthIdentity, error)
	GetUserByVerifiedEmail(email string) (*database.User, error)
	GetUserByUsername(username string) (*database.User, error)
	GetUserByID(id string) (*database.User, error)
	CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*database.User, error)
}

// ResolveUser turns a verified IdentityClaims into a local user, applying the security
// gates IN ORDER:
//  1. Reject if the IdP did not verify the email (never trust an unverified address).
//  2. Reject if the email is not on the allowlist — a valid IdP login by an
//     unauthorized account must NOT create a user or a session (verified ≠ authorized).
//  3. Resolve by (provider, subject) → existing linked user. When the IdP's
//     verified email is that user's current email, it is recorded as verified
//     (database.VerifiedEmailMarker, best-effort).
//  4. Else resolve by an account's VERIFIED email → auto-link this identity
//     to that user. A self-set profile email is never matched: it would let
//     anyone capture another person's first SSO login. With a
//     database.VerifiedEmailIdentityLinker the lookup and the link are one
//     locked store operation, so the holder cannot change between them.
//  5. Else auto-create a user (DefaultRole) + link the identity. If accounts
//     already hold the address UNVERIFIED, the verified login wins: in one
//     batch the address is cleared from them (they keep their accounts) and
//     the new user is created with it (database.UnverifiedEmailDisplacer).
//     That is logged at Warn with the account ids and reported through
//     Config.OnEmailDisplaced for the admin's audit log. If an account holds
//     it VERIFIED, the login is denied (ErrEmailConflict). A store without
//     the displacer creates the user next to the unverified copies, which
//     stay unverified.
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
		if util.SameEmail(user.Email, claims.Email) {
			if m, ok := database.AsCapability[database.VerifiedEmailMarker](store); ok {
				// Best-effort: the login itself is already decided. A failure
				// only means the address stays unverified for now.
				if _, err := m.MarkUserEmailVerified(user.ID, claims.Email); err != nil {
					resolveLog.Warn("oauth resolve: could not record the verified email for user %s: %v", user.ID, err)
				}
			}
		}
		return user, nil
	}

	// (4) Link to an existing user whose email is verified and the same.
	existing, err := c.linkToVerifiedEmailHolder(store, claims)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	// (5) Auto-create a new user + link the identity.
	username := c.uniqueUsername(store, claims)
	user, err := c.createUserForLogin(store, username, claims)
	if err != nil {
		return nil, fmt.Errorf("oauth resolve: create user: %w", err)
	}
	if m, ok := database.AsCapability[database.VerifiedEmailMarker](store); ok {
		// Records the address as IdP-verified (EmailSource "idp"); CreateUser
		// alone marks it as set up. Best-effort, as in step 3.
		if _, err := m.MarkUserEmailVerified(user.ID, claims.Email); err != nil {
			resolveLog.Warn("oauth resolve: could not record the verified email for new user %s: %v", user.ID, err)
		}
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

// linkToVerifiedEmailHolder is ResolveUser step 4: it links the identity to
// the account holding its address proven and returns that account, or nil
// when there is none.
func (c *Config) linkToVerifiedEmailHolder(store UserStore, claims IdentityClaims) (*database.User, error) {
	identity := &database.OAuthIdentity{Provider: claims.Provider, Subject: claims.Subject, Email: claims.Email}
	if l, ok := database.AsCapability[database.VerifiedEmailIdentityLinker](store); ok {
		user, err := l.LinkIdentityToVerifiedEmailHolder(identity, claims.Email)
		if err != nil {
			return nil, fmt.Errorf("oauth resolve: link identity by verified email: %w", err)
		}
		return user, nil
	}
	// A store without the locked link (a test double): look up, then link.
	existing, err := store.GetUserByVerifiedEmail(claims.Email)
	if err != nil {
		return nil, fmt.Errorf("oauth resolve: lookup user by email: %w", err)
	}
	if existing == nil {
		return nil, nil
	}
	identity.UserID = existing.ID
	if _, err := store.CreateOAuthIdentity(identity); err != nil {
		return nil, fmt.Errorf("oauth resolve: link identity: %w", err)
	}
	return existing, nil
}

// createUserForLogin creates the login's user (ResolveUser step 5), taking
// its IdP-verified address back from accounts that only typed it in when the
// store can (database.UnverifiedEmailDisplacer). An account holding the
// address proven denies the login (ErrEmailConflict).
func (c *Config) createUserForLogin(store UserStore, username string, claims IdentityClaims) (*database.User, error) {
	d, ok := database.AsCapability[database.UnverifiedEmailDisplacer](store)
	if !ok {
		user, err := store.CreateUser(username, claims.Email, "oauth", "", []string{c.DefaultRole}, "active")
		if errors.Is(err, database.ErrUserEmailTaken) {
			return nil, ErrEmailConflict
		}
		return user, err
	}
	user, displaced, err := d.CreateUserDisplacingUnverifiedEmail(username, claims.Email, "oauth", "", []string{c.DefaultRole}, "active")
	if errors.Is(err, database.ErrUserEmailTaken) {
		return nil, ErrEmailConflict
	}
	if err != nil {
		return nil, err
	}
	if len(displaced) == 0 {
		return user, nil
	}
	resolveLog.Warn("oauth resolve: verified %s login took its email back from unverified account(s) %v; new account %s",
		logger.SanitizeLogValue(claims.Provider), displaced, user.ID)
	if c.OnEmailDisplaced != nil {
		c.OnEmailDisplaced(user.ID, displaced, claims.Provider)
	}
	return user, nil
}
