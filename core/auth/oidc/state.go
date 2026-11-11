// file: core/auth/oidc/state.go
// version: 1.0.0
// guid: bd146366-8bb0-4d7e-b11c-3789b7f3b40d
// last-edited: 2026-11-11

package oidc

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// StateKeyInfo is the info string the state key is derived under
// (secrets.Store.DeriveKey): the derive key survives master-key rotation
// (SEC-28), so a rotation does not invalidate an in-flight sign-in.
const StateKeyInfo = "mediacore/auth/oidc-state/v1"

// StateTTL is how long a started sign-in may take to come back.
const StateTTL = 10 * time.Minute

// futureSkew is how far ahead of the clock a state's issue time may be.
const futureSkew = time.Minute

// KeyDeriver derives a 32-byte key for an info string. *secrets.Store is one.
type KeyDeriver interface {
	DeriveKey(info string) ([]byte, error)
}

// Payload is what a sign-in carries across the provider redirect: the anti-CSRF
// state, the PKCE verifier, the OIDC nonce, and, for a native-app sign-in, the
// hand-off the callback honours. The codec signs it; the caller keeps it in a
// short-lived cookie (the browser binding) and checks the callback's state
// parameter against it.
//
// Two PKCE exchanges run in a hand-off and must not be conflated: Verifier is
// server <-> provider (we choose it and redeem with it); AppChallenge is
// app <-> server (the app chose its verifier and keeps it; we only ever hold
// the challenge and hand it to the hand-off's code store). Reusing Verifier for
// the app hop would issue codes with no client-side proof of possession.
type Payload struct {
	ID       string `json:"i"` // single-use id
	State    string `json:"s"`
	Verifier string `json:"v"`
	Nonce    string `json:"n"`
	Provider string `json:"p"`
	Return   string `json:"r,omitempty"` // a same-site post-login path
	IssuedAt int64  `json:"t"`

	// AppRedirectURI only ever holds a value the hand-off allowed at start.
	AppRedirectURI string `json:"au,omitempty"`
	AppChallenge   string `json:"ac,omitempty"`
	AppState       string `json:"as,omitempty"`
}

// StateCodec signs and verifies Payloads.
type StateCodec struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

// CodecOption configures a StateCodec.
type CodecOption func(*StateCodec)

// WithClock replaces the codec's clock (a test seam, and the service clock).
func WithClock(now func() time.Time) CodecOption { return func(c *StateCodec) { c.now = now } }

// NewStateCodec builds a codec on key, which must be at least 32 bytes.
func NewStateCodec(key []byte, opts ...CodecOption) (*StateCodec, error) {
	if len(key) < 32 {
		return nil, errors.New("oidc: state key must be at least 32 bytes")
	}
	c := &StateCodec{key: append([]byte(nil), key...), ttl: StateTTL, now: time.Now}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// NewStateCodecFrom derives the codec's key under StateKeyInfo.
func NewStateCodecFrom(d KeyDeriver, opts ...CodecOption) (*StateCodec, error) {
	key, err := d.DeriveKey(StateKeyInfo)
	if err != nil {
		return nil, fmt.Errorf("oidc: derive the state key: %w", err)
	}
	return NewStateCodec(key, opts...)
}

// Encode signs p into base64url(json).base64url(hmac). It stamps IssuedAt
// when zero.
func (c *StateCodec) Encode(p Payload) (string, error) {
	if p.IssuedAt == 0 {
		p.IssuedAt = c.now().Unix()
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("oidc: encode state: %w", err)
	}
	b64 := base64.RawURLEncoding.EncodeToString(body)
	return b64 + "." + base64.RawURLEncoding.EncodeToString(c.mac(b64)), nil
}

// Decode verifies the signature and the age and returns the payload. A
// tampered, malformed, expired (older than the ttl) or future-dated blob is an
// error, and so is a payload with no id: ErrStateInvalid.
func (c *StateCodec) Decode(blob string) (*Payload, error) {
	body64, mac64, ok := strings.Cut(blob, ".")
	if !ok || body64 == "" || mac64 == "" {
		return nil, fmt.Errorf("%w: malformed", ErrStateInvalid)
	}
	got, err := base64.RawURLEncoding.DecodeString(mac64)
	if err != nil || !hmac.Equal(got, c.mac(body64)) {
		return nil, fmt.Errorf("%w: signature", ErrStateInvalid)
	}
	body, err := base64.RawURLEncoding.DecodeString(body64)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding", ErrStateInvalid)
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("%w: payload", ErrStateInvalid)
	}
	now := c.now().Unix()
	switch {
	case now-p.IssuedAt > int64(c.ttl/time.Second):
		return nil, fmt.Errorf("%w: expired", ErrStateInvalid)
	case p.IssuedAt-now > int64(futureSkew/time.Second):
		return nil, fmt.Errorf("%w: issued in the future", ErrStateInvalid)
	case p.ID == "" || p.State == "":
		return nil, fmt.Errorf("%w: incomplete", ErrStateInvalid)
	}
	return &p, nil
}

func (c *StateCodec) mac(msg string) []byte {
	h := hmac.New(sha256.New, c.key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

// sameSecret compares two secrets in constant time.
func sameSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// randToken returns n bytes of crypto-random data as base64url.
func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oidc: entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CodeChallengeS256 derives the PKCE S256 challenge from a verifier.
func CodeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// consumed is the set of spent state ids, each kept until its state would have
// expired anyway. It is instance state, bounded, and per process: a state is
// single use within the process that issued the cookie, and a provider's code
// is single use at the provider, so a replay across a restart still fails at
// the exchange.
type consumed struct {
	mu   sync.Mutex
	ids  map[string]time.Time
	max  int
	last time.Time
}

func newConsumed() *consumed { return &consumed{ids: map[string]time.Time{}, max: 4096} }

// take spends id until expires and reports whether this call spent it: false
// when it was already spent. When the set is full of live ids the oldest
// entries are evicted last-resort: a state older than its ttl is refused by
// the codec anyway, so eviction only ever shortens "spent" for an id that is
// about to expire.
func (c *consumed) take(id string, now, expires time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.last) > time.Minute || len(c.ids) >= c.max {
		for k, exp := range c.ids {
			if !exp.After(now) {
				delete(c.ids, k)
			}
		}
		c.last = now
	}
	if exp, spent := c.ids[id]; spent && exp.After(now) {
		return false
	}
	if len(c.ids) >= c.max {
		// Full of live ids: drop the one expiring first.
		var oldest string
		var at time.Time
		for k, exp := range c.ids {
			if oldest == "" || exp.Before(at) {
				oldest, at = k, exp
			}
		}
		delete(c.ids, oldest)
	}
	c.ids[id] = expires
	return true
}
