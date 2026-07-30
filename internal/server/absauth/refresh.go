// file: internal/server/absauth/refresh.go
// version: 1.0.0
// guid: 1d78e0b4-5a92-4c36-8f01-6b2c9d47a5e8
// last-edited: 2026-07-30

package absauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
)

// RefreshTokenPrefix marks an ABS refresh token, exactly as spec §3.2 specifies.
// It is distinct from the existing `abk_` API-key prefix so the two schemes can
// never be confused for one another.
const RefreshTokenPrefix = "abr_"

// refreshSeedBytes is the size of the per-session random seed.
const refreshSeedBytes = 32

// NewRefreshSeed returns fresh per-session random material for refresh-token
// derivation. It is stored on the abs_sess record; on its own it is not a credential.
func NewRefreshSeed() (string, error) {
	buf := make([]byte, refreshSeedBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("absauth: read random seed: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// DeriveRefreshToken returns this session's refresh token for the given generation.
//
// The token is `abr_` + base64url of an HMAC-SHA256 over (sessionID, seed,
// generation) keyed by ABS_JWT_SECRET, so it is a 43-character opaque string
// indistinguishable from 32 random bytes — exactly the shape spec §3.2 describes —
// while being reproducible by the server.
//
// Reproducibility is what makes §3.4 step 3 possible WITHOUT ever storing a live
// credential. When a device replays the previous refresh token inside the grace
// window we must hand back the already-minted current pair rather than rotating
// again; deriving it means the database never holds a refresh token in plaintext, so
// a stolen database copy yields nothing without the environment secret.
//
// Consequences that are deliberate:
//   - Two different sessions never collide, because sessionID is in the MAC input.
//   - A rotated generation is unguessable from its predecessor.
//   - Rotating ABS_JWT_SECRET invalidates every refresh token as well as every
//     access token, which is the intended "log everyone out" lever.
func (c *Config) DeriveRefreshToken(sessionID, seed string, generation int) string {
	mac := hmac.New(sha256.New, c.secret)
	// Length-prefix-free but unambiguous: seed and sessionID are base64url/ULID text,
	// neither of which can contain ':'.
	mac.Write([]byte(sessionID))
	mac.Write([]byte{':'})
	mac.Write([]byte(seed))
	mac.Write([]byte{':'})
	mac.Write([]byte(strconv.Itoa(generation)))
	return RefreshTokenPrefix + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// HashRefreshToken returns the SHA-256 hex of a refresh token. Only this hash is
// persisted (spec §3.2), and it is what the refresh path compares against.
func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqualHash compares two refresh-token hashes without leaking timing
// information about how many leading characters matched.
func ConstantTimeEqualHash(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return hmac.Equal([]byte(a), []byte(b))
}
