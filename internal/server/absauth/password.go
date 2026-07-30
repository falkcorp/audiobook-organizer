// file: internal/server/absauth/password.go
// version: 1.0.0
// guid: 7b26c8f1-3e40-49da-95b7-0c48f2a6d371
// last-edited: 2026-07-30

package absauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Password-hash algorithm identifiers as stored on database.User.PasswordHashAlgo.
const (
	// AlgoArgon2id is what every NEW or rehashed password uses (spec §3.5).
	AlgoArgon2id = "argon2id"
	// AlgoBcrypt is what existing users have. Verification keeps working and a
	// successful login triggers a transparent rehash, so the migration needs no
	// flag day and no password reset.
	AlgoBcrypt = "bcrypt"
	// AlgoOAuth marks a user with NO password credential — created by the OAuth
	// login or by ABS Cloudflare-Access JIT provisioning. Such a user must never
	// authenticate by password.
	AlgoOAuth = "oauth"
)

// argon2id parameters. m=64 MiB / t=3 / p=2 is the OWASP-recommended baseline and
// costs a few tens of milliseconds per login, which is acceptable because logins are
// rare (access tokens last 30 days).
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
	argonVersion        = argon2.Version
)

// HashPassword hashes a new password with argon2id and returns the algorithm
// identifier alongside the PHC-encoded hash.
func HashPassword(plain string) (algo string, hash string, err error) {
	if plain == "" {
		return "", "", fmt.Errorf("absauth: refusing to hash an empty password")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", "", fmt.Errorf("absauth: read password salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argonVersion, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	return AlgoArgon2id, encoded, nil
}

// VerifyPassword checks plain against a stored hash.
//
// ok reports whether the password is correct. needsRehash reports whether the caller
// should re-store the password with HashPassword after a successful login — true for
// every legacy bcrypt hash, which is how spec §3.5's "rehash-on-successful-login"
// migration to argon2id happens without a flag day.
//
// SECURITY: a user with an EMPTY stored hash never authenticates, whatever the
// algorithm or supplied password. That is the case for OAuth and
// Cloudflare-Access-JIT users, who have no password credential at all — treating an
// empty hash as "matches empty password" would make every such account trivially
// loginable. Likewise an explicit "oauth" algorithm never authenticates by password.
func VerifyPassword(algo, hash, plain string) (ok bool, needsRehash bool) {
	if strings.TrimSpace(hash) == "" {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case AlgoOAuth:
		return false, false
	case AlgoArgon2id:
		return verifyArgon2id(hash, plain), false
	case AlgoBcrypt, "":
		// An empty algo predates the column being populated; those rows are bcrypt.
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
			return false, false
		}
		return true, true
	default:
		// An unknown algorithm is a fail-closed condition, not a fallback.
		return false, false
	}
}

// verifyArgon2id parses a PHC-encoded argon2id hash and compares in constant time.
// Any malformed field is a verification failure, never a panic and never a pass.
func verifyArgon2id(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[0] != "" || parts[1] != AlgoArgon2id {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argonVersion {
		return false
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	if memory == 0 || time == 0 || threads == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(plain), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
