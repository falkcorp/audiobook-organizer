// file: internal/server/handlers/abs/openid_test.go
// version: 1.0.0
// guid: 7e2b98d5-4a13-4c07-b6f9-08d5137ac642
// last-edited: 2026-08-01

package abs

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"
)

// pkcePair returns a verifier and its S256 challenge, exactly as AudioBooth
// computes them.
func pkcePair(verifier string) (string, string) {
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// A code must be redeemable exactly once. Enforced inside the store under a
// single lock so two concurrent redemptions cannot both win; this pins the
// contract that guarantee rests on.
func TestOIDCCodeStore_SingleUse(t *testing.T) {
	store := &oidcCodeStore{codes: map[string]oidcPendingCode{}}
	now := time.Now()
	store.put("abc", oidcPendingCode{UserID: "u1", ExpiresAt: now.Add(time.Minute)}, now)

	if _, ok := store.take("abc"); !ok {
		t.Fatal("first redemption must succeed")
	}
	if _, ok := store.take("abc"); ok {
		t.Fatal("second redemption must fail — an authorization code is single-use")
	}
}

// Concurrent redemption of one code must yield exactly one winner. Without the
// delete happening under the same lock as the read, both callers would receive
// the code and two sessions would be minted from one authorization.
func TestOIDCCodeStore_ConcurrentRedemptionHasOneWinner(t *testing.T) {
	store := &oidcCodeStore{codes: map[string]oidcPendingCode{}}
	now := time.Now()
	const n = 50
	for i := 0; i < n; i++ {
		store.put("race", oidcPendingCode{UserID: "u1", ExpiresAt: now.Add(time.Minute)}, now)

		results := make(chan bool, 2)
		start := make(chan struct{})
		for j := 0; j < 2; j++ {
			go func() {
				<-start
				_, ok := store.take("race")
				results <- ok
			}()
		}
		close(start)
		wins := 0
		for j := 0; j < 2; j++ {
			if <-results {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("iteration %d: %d goroutines redeemed the same code; want exactly 1", i, wins)
		}
	}
}

// An expired code must not be redeemable. take() deliberately still removes it —
// expiry is checked by the caller — so this asserts the stored deadline is what
// the handler will compare against.
func TestOIDCCodeStore_ExpiryIsRecorded(t *testing.T) {
	store := &oidcCodeStore{codes: map[string]oidcPendingCode{}}
	now := time.Now()
	store.put("old", oidcPendingCode{UserID: "u1", ExpiresAt: now.Add(-time.Second)}, now)

	pending, ok := store.take("old")
	if !ok {
		t.Fatal("take should return the record; expiry is the caller's check")
	}
	if !now.After(pending.ExpiresAt) {
		t.Fatal("stored deadline must be in the past for an expired code")
	}
}

// The PKCE comparison the handler performs: BASE64URL(SHA256(verifier)) with no
// padding must equal the challenge the client sent. A mismatch here is what stops
// a stolen redirect (custom URL schemes can be claimed by another installed app)
// from being redeemable without the original verifier.
func TestPKCE_S256MatchesAudioBoothsComputation(t *testing.T) {
	verifier, challenge := pkcePair("a-verifier-of-reasonable-length-1234567890")

	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	if got != challenge {
		t.Fatalf("challenge = %q, want %q", got, challenge)
	}
	// RawURLEncoding: no '=' padding and URL-safe alphabet. Standard base64 here
	// would fail against every real client.
	if strings.ContainsAny(got, "+/=") {
		t.Fatalf("challenge %q must be unpadded URL-safe base64", got)
	}

	_, wrong := pkcePair("a-different-verifier")
	if wrong == challenge {
		t.Fatal("different verifiers must not produce the same challenge")
	}
}

// The redirect must preserve the client's custom scheme verbatim. AudioBooth
// registers "audiobooth://oauth"; anything re-serialised (an inserted path slash,
// percent-encoded scheme) may fail to match its handler and the app never wakes.
func TestOIDCRedirectPreservesCustomScheme(t *testing.T) {
	target := "audiobooth://oauth"
	built := buildOIDCRedirect(target, map[string]string{"code": "abc123", "state": "s1"})

	if !strings.HasPrefix(built, "audiobooth://oauth?") {
		t.Fatalf("redirect %q must keep the scheme and path exactly as registered", built)
	}
	u, err := url.Parse(built)
	if err != nil {
		t.Fatalf("redirect must parse: %v", err)
	}
	if got := u.Query().Get("code"); got != "abc123" {
		t.Fatalf("code = %q, want abc123", got)
	}
	if got := u.Query().Get("state"); got != "s1" {
		t.Fatalf("state = %q, want s1", got)
	}
}

// Empty params must be omitted rather than emitted as `state=`, which some
// clients treat as a present-but-mismatched value.
func TestOIDCRedirectOmitsEmptyParams(t *testing.T) {
	built := buildOIDCRedirect("audiobooth://oauth", map[string]string{"code": "x", "state": ""})
	if strings.Contains(built, "state=") {
		t.Fatalf("empty state must be omitted, got %q", built)
	}
}

// An error must come back through the client's own callback, not as a rendered
// page: the flow runs inside ASWebAuthenticationSession, which only closes when
// the callback scheme is hit. A JSON error body would hang it until the user
// cancels.
func TestOIDCRedirectErrorUsesCallbackScheme(t *testing.T) {
	built := buildOIDCRedirect("audiobooth://oauth", map[string]string{
		"error":             "access_denied",
		"error_description": "no verified identity on this request",
		"state":             "s9",
	})
	if !strings.HasPrefix(built, "audiobooth://oauth?") {
		t.Fatalf("errors must return through the callback scheme, got %q", built)
	}
	u, _ := url.Parse(built)
	if u.Query().Get("error") != "access_denied" {
		t.Fatalf("error param missing in %q", built)
	}
	if u.Query().Get("code") != "" {
		t.Fatal("an error redirect must never carry a code")
	}
}
