// file: internal/oauth/state.go
// version: 1.0.0
// guid: 1d6f8b03-4a29-4c57-9e08-2b7a5c0e3d94
// last-edited: 2026-07-26

package oauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StatePayload is the anti-CSRF + PKCE material carried across the OAuth redirect. The
// handler serializes it into a short-TTL, HMAC-signed cookie before redirecting to the
// IdP, then verifies it on callback: the `state` query param must equal Payload.State
// (CSRF), and Payload.Verifier is the PKCE code verifier sent in the token exchange.
type StatePayload struct {
	State     string `json:"s"`
	Verifier  string `json:"v"`
	Provider  string `json:"p"`
	Return    string `json:"r,omitempty"` // optional post-login return path
	IssuedAt  int64  `json:"t"`
}

// StateCodec signs/verifies StatePayload blobs with an HMAC secret. A fresh random
// secret is generated per process, so an in-flight login started before a restart
// simply fails verification and the user retries — acceptable for a login handshake.
type StateCodec struct {
	secret []byte
	ttl    time.Duration
}

// NewStateCodec creates a codec with a random per-process secret and the given TTL.
func NewStateCodec(ttl time.Duration) (*StateCodec, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("oauth: generate state secret: %w", err)
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &StateCodec{secret: secret, ttl: ttl}, nil
}

// Encode signs a payload into a URL-safe cookie value: base64(json).base64(hmac).
func (c *StateCodec) Encode(p StatePayload) (string, error) {
	if p.IssuedAt == 0 {
		p.IssuedAt = time.Now().Unix()
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(body)
	mac := c.sign(b64)
	return b64 + "." + mac, nil
}

// Decode verifies the HMAC + TTL and returns the payload. Any tampering, a bad
// signature, or an expired token is an error.
func (c *StateCodec) Decode(blob string) (*StatePayload, error) {
	parts := strings.SplitN(blob, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("oauth: malformed state")
	}
	if !hmac.Equal([]byte(parts[1]), []byte(c.sign(parts[0]))) {
		return nil, fmt.Errorf("oauth: state signature mismatch")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("oauth: state decode: %w", err)
	}
	var p StatePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("oauth: state unmarshal: %w", err)
	}
	if time.Since(time.Unix(p.IssuedAt, 0)) > c.ttl {
		return nil, fmt.Errorf("oauth: state expired")
	}
	return &p, nil
}

func (c *StateCodec) sign(msg string) string {
	h := hmac.New(sha256.New, c.secret)
	h.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// randToken returns n bytes of crypto-random data as a URL-safe string.
func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateState returns a random anti-CSRF state token.
func GenerateState() (string, error) { return randToken(24) }

// GenerateCodeVerifier returns a PKCE code verifier (RFC 7636: 43-128 chars).
func GenerateCodeVerifier() (string, error) { return randToken(48) }

// CodeChallengeS256 derives the PKCE S256 challenge from a verifier.
func CodeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
