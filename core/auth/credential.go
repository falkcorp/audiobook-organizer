// file: core/auth/credential.go
// version: 1.0.0
// guid: a945d114-9800-4a9d-8de2-e3962ca06406
// last-edited: 2026-11-10

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"slices"
	"strings"
)

// CredentialKind is the type of a core credential, named by its prefix
// (05-authn §4.1).
type CredentialKind string

// The native prefixes are core's, not per-app: one binary may host several
// modules, and a key is a core credential (§4.1).
const (
	KindSession   CredentialKind = "mcs_"
	KindAPIKey    CredentialKind = "mck_"
	KindBootstrap CredentialKind = "mcb_"
	KindInvite    CredentialKind = "mci_"
	KindTempLogin CredentialKind = "mct_"
)

const (
	secretBytes    = 32
	secretChars    = 43 // base64url, no padding, of 32 bytes
	checksumChars  = 6  // base62 of a CRC32 (62^6 > 2^32)
	prefixChars    = 4
	credentialLen  = prefixChars + secretChars + 1 + checksumChars
	base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// MintCredential returns a fresh credential of kind: the prefix, 32 bytes
// from crypto/rand encoded base64url without padding (43 characters), "_",
// and the 6-character base62 CRC32 of everything before it, so a secret
// scanner can recognise and offline-validate a leaked value without a
// database (§4.1).
func MintCredential(kind CredentialKind) (string, error) {
	if !kind.native() {
		return "", fmt.Errorf("%w: mint of non-native prefix %q", ErrBadInput, kind)
	}
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: read credential randomness: %w", err)
	}
	body := string(kind) + base64.RawURLEncoding.EncodeToString(buf)
	return body + "_" + checksum(body), nil
}

// nativeKinds are the five native prefixes.
func nativeKinds() []CredentialKind {
	return []CredentialKind{KindSession, KindAPIKey, KindBootstrap, KindInvite, KindTempLogin}
}

// native reports whether k is one of the five native prefixes.
func (k CredentialKind) native() bool { return slices.Contains(nativeKinds(), k) }

// checksum is the base62 CRC32 (IEEE) of body, zero-padded to 6 characters.
func checksum(body string) string {
	n := crc32.ChecksumIEEE([]byte(body))
	var out [checksumChars]byte
	for i := checksumChars - 1; i >= 0; i-- {
		out[i] = base62Alphabet[n%62]
		n /= 62
	}
	return string(out[:])
}

// ParseCredential classifies a presented value against kind (§4.1, §10
// TestCredentialFormat_ChecksumAndPrefix):
//
//   - a value that does not start with kind's prefix is not a credential of
//     that kind: ErrNoCredential, so the next authenticator may try;
//   - a value with the prefix whose length, alphabet or checksum is wrong is
//     a presented-but-invalid credential: ErrInvalidCredentials, which never
//     falls through to a weaker one (SEC-12);
//   - otherwise nil: the value is well formed (whether it is known is the
//     store's lookup).
func ParseCredential(kind CredentialKind, raw string) error {
	if !strings.HasPrefix(raw, string(kind)) {
		return ErrNoCredential
	}
	if !kind.native() {
		return fmt.Errorf("%w: %q is not a native prefix", ErrInvalidCredentials, kind)
	}
	if len(raw) != credentialLen {
		return fmt.Errorf("%w: %s credential of the wrong length", ErrInvalidCredentials, kind)
	}
	body, sep, sum := raw[:prefixChars+secretChars], raw[prefixChars+secretChars], raw[prefixChars+secretChars+1:]
	if sep != '_' {
		return fmt.Errorf("%w: %s credential without its checksum separator", ErrInvalidCredentials, kind)
	}
	secret := body[prefixChars:]
	dec, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	if err != nil || len(dec) != secretBytes {
		return fmt.Errorf("%w: %s credential secret is not 32 base64url bytes", ErrInvalidCredentials, kind)
	}
	if sum != checksum(body) {
		return fmt.Errorf("%w: %s credential checksum mismatch", ErrInvalidCredentials, kind)
	}
	return nil
}

// HashCredential is the stored form of a credential: SHA-256 of the whole raw
// value (§4.1). 256-bit random input makes a slow KDF unnecessary. An
// imported key is hashed the same way, so ABO's abk_ rows keep verifying
// (§11).
func HashCredential(raw string) [32]byte { return sha256.Sum256([]byte(raw)) }

// HashHex is HashCredential in lower-case hex, the store's index form.
func HashHex(raw string) string {
	h := HashCredential(raw)
	return hex.EncodeToString(h[:])
}

// hasImportedPrefix reports whether raw starts with one of prefixes.
func hasImportedPrefix(prefixes []string, raw string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(raw, p) {
			return true
		}
	}
	return false
}
