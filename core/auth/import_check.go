// file: core/auth/import_check.go
// version: 1.0.0
// guid: 28aab425-4187-44a3-965e-f9aedc5169a2
// last-edited: 2026-11-12

package auth

import (
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/rbac"
)

// The Importer's record rules as predicates (§11). Importer implementations
// (core/internal/authstore) call them first, so the rules an Import* applies
// to a record's own fields live in one place; an app's cutover preflight
// calls the same predicates to run "the importer's validation without
// staging" before any store exists (05-storage §5S.3.13 "Cutover blockers").
// They look at the record alone: whether the owner is stored, whether the ID
// or the username is taken and whether the batch can hold the write are the
// store's, and a preflight over HEAD's rows decides them from HEAD's own rows.
//
// Every refusal wraps ErrImportRejected and names the record by ID, never by
// a value: a password hash, a token hash and a scope list never appear in the
// text.

func importReject(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrImportRejected, fmt.Sprintf(format, a...))
}

// importableStatus reports a user status the store may hold.
func importableStatus(st UserStatus) bool {
	switch st {
	case StatusActive, StatusDisabled, StatusPending, StatusSystem:
		return true
	}
	return false
}

// CheckImportUser is Importer.ImportUser's rule for u and its stored hash: an
// ID and a username, a known status, and a hash that is argon2id or bcrypt
// (or empty: an account without a password).
func CheckImportUser(u *User, passwordHash string) error {
	if u == nil || u.ID == "" || strings.TrimSpace(u.Username) == "" {
		return importReject("a user needs an ID and a username")
	}
	if !importableStatus(u.Status) {
		return importReject("user %s has status %q", u.ID, u.Status)
	}
	if passwordHash != "" {
		if a := password.AlgoOf(passwordHash); a != password.AlgoArgon2id && a != password.AlgoBcrypt {
			return importReject("user %s: password hash is not argon2id or bcrypt", u.ID)
		}
	}
	return nil
}

// CheckImportAPIKey is Importer.ImportAPIKey's rule for k and the SHA-256 of
// its raw value: an ID, at least one parseable concrete scope, a known status,
// a token hash, and an expiry unless the key is revoked (a key whose RevokedAt
// is set counts as revoked whatever its Status says: early fix 3's most
// restrictive reading).
func CheckImportAPIKey(k *APIKey, tokenSHA256 [32]byte) error {
	if k == nil || k.ID == "" {
		return importReject("a key needs an ID")
	}
	if len(k.Scopes) == 0 {
		return importReject("apikey %s: an API key holds at least one scope", k.ID)
	}
	for _, p := range k.Scopes {
		if _, err := rbac.ParsePermission(string(p)); err != nil {
			return importReject("apikey %s: scope %q: %v", k.ID, p, err)
		}
	}
	if !k.Status.valid() {
		return importReject("apikey %s has status %q", k.ID, k.Status)
	}
	if tokenSHA256 == ([32]byte{}) {
		return importReject("apikey %s has no token hash", k.ID)
	}
	revoked := k.Status == KeyRevoked || !k.RevokedAt.IsZero()
	if !revoked && k.ExpiresAt.IsZero() {
		return importReject("apikey %s has no expiry", k.ID)
	}
	return nil
}

// CheckImportIdentity is Importer.ImportIdentity's rule for id: a provider
// without a slash, a subject and a user.
func CheckImportIdentity(id *ExternalIdentity) error {
	if id == nil || id.Provider == "" || id.Subject == "" || id.UserID == "" {
		return importReject("an identity needs a provider, a subject and a user")
	}
	if strings.Contains(id.Provider, "/") {
		return importReject("provider %q holds a slash", id.Provider)
	}
	return nil
}
