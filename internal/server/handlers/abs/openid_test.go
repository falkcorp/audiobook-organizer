// file: internal/server/handlers/abs/openid_test.go
// version: 2.0.0
// guid: 7e2b98d5-4a13-4c07-b6f9-08d5137ac642
// last-edited: 2026-10-19

package abs

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
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

// A key must be redeemable exactly once. Enforced inside the store under a
// single lock so two concurrent redemptions cannot both win; this pins the
// contract that guarantee rests on. The same store backs minted codes and the
// parked hop-1 requests.
func TestOIDCTTLStore_SingleUse(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingCode](8)
	now := time.Now()
	if err := store.put("abc", oidcPendingCode{UserID: "u1"}, now, time.Minute); err != nil {
		t.Fatal(err)
	}

	if !store.live("abc", now) {
		t.Fatal("live must report an unexpired entry")
	}
	if !store.live("abc", now) {
		t.Fatal("live must not consume the entry")
	}
	if _, ok := store.take("abc", now); !ok {
		t.Fatal("first redemption must succeed")
	}
	if _, ok := store.take("abc", now); ok {
		t.Fatal("second redemption must fail — an authorization code is single-use")
	}
	if store.live("abc", now) {
		t.Fatal("a taken entry must not be live")
	}
}

// Concurrent redemption of one key must yield exactly one winner. Without the
// delete happening under the same lock as the read, both callers would receive
// the entry and two sessions would be minted from one authorization.
func TestOIDCTTLStore_ConcurrentRedemptionHasOneWinner(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingCode](8)
	now := time.Now()
	const n = 50
	for i := range n {
		if err := store.put("race", oidcPendingCode{UserID: "u1"}, now, time.Minute); err != nil {
			t.Fatal(err)
		}

		results := make(chan bool, 2)
		start := make(chan struct{})
		for range 2 {
			go func() {
				<-start
				_, ok := store.take("race", now)
				results <- ok
			}()
		}
		close(start)
		wins := 0
		for range 2 {
			if <-results {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("iteration %d: %d goroutines redeemed the same key; want exactly 1", i, wins)
		}
	}
}

// An expired entry is not redeemable, and the failed attempt deletes it, so it
// cannot be retried later under a skewed clock either. The boundary itself is
// expired: a TTL of one minute means usable for strictly less than a minute.
func TestOIDCTTLStore_ExpiredIsRefusedAndDeleted(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingCode](8)
	now := time.Now()
	if err := store.put("old", oidcPendingCode{UserID: "u1"}, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if store.live("old", now.Add(time.Minute)) {
		t.Fatal("an entry at its expiry instant must not be live")
	}
	if _, ok := store.take("old", now.Add(time.Minute)); ok {
		t.Fatal("an expired entry must not be redeemable")
	}
	if _, ok := store.take("old", now); ok {
		t.Fatal("the expired entry must have been deleted by the failed take")
	}
}

// The store is bounded: at its cap a NEW key is refused rather than evicting a
// live one, expired entries are swept to make room, and re-putting an existing
// key is allowed. Hop 1 is reachable without an account, so without the cap it
// is an unbounded map an attacker can fill.
func TestOIDCTTLStore_BoundedRefusesWhenFull(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingRequest](2)
	now := time.Now()
	if err := store.put("a", oidcPendingRequest{}, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.put("b", oidcPendingRequest{}, now, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.put("c", oidcPendingRequest{}, now, time.Minute); !errors.Is(err, errOIDCStoreFull) {
		t.Fatalf("put at cap = %v, want errOIDCStoreFull", err)
	}
	if !store.live("a", now) || !store.live("b", now) {
		t.Fatal("a refused put must not evict live entries")
	}
	if err := store.put("a", oidcPendingRequest{}, now, time.Minute); err != nil {
		t.Fatalf("re-putting an existing key at cap must succeed: %v", err)
	}
	// "b" expires; the sweep on the next put at cap frees its slot.
	later := now.Add(11 * time.Second)
	if err := store.put("c", oidcPendingRequest{}, later, time.Minute); err != nil {
		t.Fatalf("put after an entry expired must succeed: %v", err)
	}
	if len(store.entries) != 2 {
		t.Fatalf("store holds %d entries, want 2 (cap)", len(store.entries))
	}
}

// Every S256 challenge, and every request id, is 43 unpadded base64url chars.
func TestIsS256Shaped(t *testing.T) {
	_, good := pkcePair("a-verifier-of-reasonable-length-1234567890")
	if !isS256Shaped(good) {
		t.Fatalf("a real S256 challenge %q must be accepted", good)
	}
	for _, bad := range []string{"", good[:42], good + "A", good[:42] + "=", good[:42] + "+", good[:42] + "/", good[:42] + " "} {
		if isS256Shaped(bad) {
			t.Errorf("%q must be rejected", bad)
		}
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

// A hop-2 error must come back through the client's own callback, not as a
// rendered page: by then the flow runs inside ASWebAuthenticationSession, which
// only closes when the callback scheme is hit. (Hop 1 is the opposite: it is the
// app's own HTTP client, and it must never see the custom scheme.)
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

// ── redirect_uri allowlist: the account-takeover control ────────────────────

// An unvalidated redirect_uri is account takeover, and PKCE cannot prevent it:
// the attacker supplies the code_challenge, so they hold the verifier and can
// redeem a code minted for the victim. These are the payloads that matter.
func TestOIDCRedirectAllowlist_RejectsAttackerControlledTargets(t *testing.T) {
	t.Setenv(OIDCRedirectURIsEnvVar, "")
	hostile := []string{
		"https://evil.example",
		"https://evil.example/oauth",
		"http://127.0.0.1:1234/steal",
		// Prefix matching would admit this one.
		"audiobooth://oauth.evil.example",
		"audiobooth://oauth@evil.example",
		"audiobooth://oauth/../../evil",
		// Suffix matching would admit these.
		"https://evil.example/#audiobooth://oauth",
		"https://evil.example?x=audiobooth://oauth",
		// Case and whitespace must not open a door.
		"AUDIOBOOTH://OAUTH",
		" audiobooth://oauth",
		"audiobooth://oauth ",
		"",
		"javascript:alert(1)",
		"//evil.example",
	}
	for _, uri := range hostile {
		if oidcRedirectAllowed(uri) {
			t.Errorf("redirect_uri %q MUST be rejected — accepting it hands an authorization code to an attacker", uri)
		}
	}
}

func TestOIDCRedirectAllowlist_AcceptsRegisteredClient(t *testing.T) {
	t.Setenv(OIDCRedirectURIsEnvVar, "")
	if !oidcRedirectAllowed("audiobooth://oauth") {
		t.Fatal("the shipped client's registered callback must be accepted")
	}
}

// The env override replaces the default set rather than adding to it, so an
// operator can retire a client as well as add one.
func TestOIDCRedirectAllowlist_EnvOverrideReplaces(t *testing.T) {
	t.Setenv(OIDCRedirectURIsEnvVar, "otherapp://cb , second://cb")

	if !oidcRedirectAllowed("otherapp://cb") {
		t.Fatal("configured URI must be accepted (and surrounding whitespace trimmed)")
	}
	if !oidcRedirectAllowed("second://cb") {
		t.Fatal("second configured URI must be accepted")
	}
	if oidcRedirectAllowed("audiobooth://oauth") {
		t.Fatal("override must REPLACE the defaults, not extend them — otherwise a client can never be retired")
	}
}
