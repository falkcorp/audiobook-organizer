// file: internal/books/store/apikey_token.go
// version: 1.0.1
// guid: f3e2d1c0-b9a8-7654-3210-fedcba987654

package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// GenerateAPIKeyToken generates a cryptographically random API key token.
// Returns (rawToken, sha256HexHash, error).
// Raw token format: "abk_" + base64url(32 random bytes).
// Store only the hash; show rawToken to the user once.
func GenerateAPIKeyToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate api key token: %w", err)
	}
	raw = "abk_" + base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	return raw, hash, nil
}

// HashAPIKeyToken returns the SHA-256 hex hash of a raw token.
// Used by the auth middleware to look up keys without storing the raw token.
func HashAPIKeyToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
