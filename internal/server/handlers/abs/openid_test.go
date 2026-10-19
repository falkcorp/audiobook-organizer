// file: internal/server/handlers/abs/openid_test.go
// version: 2.6.0
// guid: 7e2b98d5-4a13-4c07-b6f9-08d5137ac642
// last-edited: 2026-10-19

package abs

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
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
	store.put("abc", "", 8, oidcPendingCode{UserID: "u1"}, now, time.Minute)

	if v, ok := store.peek("abc", now); !ok || v.UserID != "u1" {
		t.Fatal("peek must return an unexpired entry")
	}
	if _, ok := store.peek("abc", now); !ok {
		t.Fatal("peek must not consume the entry")
	}
	if _, ok := store.take("abc", now); !ok {
		t.Fatal("first redemption must succeed")
	}
	if _, ok := store.take("abc", now); ok {
		t.Fatal("second redemption must fail — an authorization code is single-use")
	}
	if _, ok := store.peek("abc", now); ok {
		t.Fatal("a taken entry must not be visible")
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
		store.put("race", "", 8, oidcPendingCode{UserID: "u1"}, now, time.Minute)

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
	store.put("old", "", 8, oidcPendingCode{UserID: "u1"}, now, time.Minute)
	if _, ok := store.peek("old", now.Add(time.Minute)); ok {
		t.Fatal("an entry at its expiry instant must not be visible")
	}
	if _, ok := store.take("old", now.Add(time.Minute)); ok {
		t.Fatal("an expired entry must not be redeemable")
	}
	if _, ok := store.take("old", now); ok {
		t.Fatal("the expired entry must have been deleted by the failed take")
	}
}

// Per-owner cap: an owner holds at most its cap of live entries, and a new one
// evicts that owner's OWN oldest — never anyone else's.
func TestOIDCTTLStore_PerOwnerCapEvictsOwnOldest(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingRequest](100)
	now := time.Now()
	store.put("victim", "email:v@example.com", 3, oidcPendingRequest{}, now, time.Minute)
	for i := range 10 {
		res := store.put(fmt.Sprintf("a%d", i), "svc:attacker", 4, oidcPendingRequest{}, now, time.Minute)
		if res.GlobalEvicted != 0 {
			t.Fatalf("put %d evicted globally below the global cap", i)
		}
	}
	if n := store.countOwnerLocked("svc:attacker"); n != 4 {
		t.Fatalf("attacker holds %d entries, cap 4", n)
	}
	for i := range 6 {
		if _, ok := store.peek(fmt.Sprintf("a%d", i), now); ok {
			t.Fatalf("a%d should have been evicted as the attacker's oldest", i)
		}
	}
	if _, ok := store.peek("victim", now); !ok {
		t.Fatal("another owner's entry must survive a flood that only hits its own cap")
	}
}

// One owner flooding all the way to the GLOBAL cap evicts only its own entries:
// at the global cap the store takes from the owner holding the most, not the
// globally oldest entry (which here is the victim's).
func TestOIDCTTLStore_FloodToGlobalCapEvictsOnlyItself(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingRequest](10)
	now := time.Now()
	store.put("victim", "email:v@example.com", 3, oidcPendingRequest{}, now, time.Minute)
	store.put("friend", "svc:friends", 256, oidcPendingRequest{}, now, time.Minute)
	for i := range 50 {
		store.put(fmt.Sprintf("flood%d", i), "svc:family", 256, oidcPendingRequest{}, now, time.Minute)
		if len(store.entries) > 10 {
			t.Fatalf("store grew to %d, cap 10", len(store.entries))
		}
	}
	for _, k := range []string{"victim", "friend"} {
		if _, ok := store.peek(k, now); !ok {
			t.Fatalf("%s was evicted by another credential's flood", k)
		}
	}
	if _, ok := store.peek("flood49", now); !ok {
		t.Fatal("the flooder's newest entry must land")
	}

	// Two flooders at the cap take from each other, largest first, and still
	// never from the small owner.
	for i := range 20 {
		res := store.put(fmt.Sprintf("other%d", i), "svc:other", 256, oidcPendingRequest{}, now, time.Minute)
		if res.GlobalEvicted > 0 && res.EvictedFrom != "svc:family" && res.EvictedFrom != "svc:other" {
			t.Fatalf("global eviction took from %q", res.EvictedFrom)
		}
	}
	if _, ok := store.peek("victim", now); !ok {
		t.Fatal("the small owner must survive two competing floods")
	}
}

// A full store evicts instead of refusing, so a flood can never pin it shut,
// and expired entries are swept before anything live is evicted.
func TestOIDCTTLStore_FullStoreEvictsNeverRefuses(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingRequest](3)
	now := time.Now()
	store.put("oldest", "o", 100, oidcPendingRequest{}, now, time.Minute)
	store.put("mid", "o", 100, oidcPendingRequest{}, now, time.Minute)
	store.put("new", "o", 100, oidcPendingRequest{}, now, time.Minute)

	res := store.put("newest", "o", 100, oidcPendingRequest{}, now, time.Minute)
	if res.GlobalEvicted != 1 {
		t.Fatalf("GlobalEvicted = %d, want 1", res.GlobalEvicted)
	}
	if _, ok := store.peek("newest", now); !ok {
		t.Fatal("a put into a full store must succeed")
	}
	if _, ok := store.peek("oldest", now); ok {
		t.Fatal("within the evicted owner, its oldest entry must go first")
	}

	store.put("short", "o", 100, oidcPendingRequest{}, now, time.Second) // evicts "mid"
	later := now.Add(2 * time.Second)
	res = store.put("after", "o", 100, oidcPendingRequest{}, later, time.Minute)
	if res.GlobalEvicted != 0 {
		t.Fatalf("GlobalEvicted = %d, want 0: the expired entry should have been swept instead", res.GlobalEvicted)
	}
	for _, k := range []string{"new", "newest", "after"} {
		if _, ok := store.peek(k, later); !ok {
			t.Fatalf("%s must still be live", k)
		}
	}
}

// Eviction stays bounded whatever the mix of owners: the store never exceeds
// its cap, no owner ever exceeds its cap, and a put always lands.
func TestOIDCTTLStore_EvictionBounded(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingRequest](16)
	now := time.Now()
	for i := range 500 {
		owner := fmt.Sprintf("o%d", i%7)
		key := fmt.Sprintf("k%d", i)
		store.put(key, owner, 3, oidcPendingRequest{}, now, time.Minute)
		if _, ok := store.peek(key, now); !ok {
			t.Fatalf("put %d did not land", i)
		}
		if len(store.entries) > 16 {
			t.Fatalf("store grew to %d entries, cap 16", len(store.entries))
		}
		per := map[string]int{}
		for _, e := range store.entries {
			per[e.owner]++
			if per[e.owner] > 3 {
				t.Fatalf("owner %s holds %d entries, cap 3", e.owner, per[e.owner])
			}
		}
	}
}

// Re-putting a key replaces it rather than counting twice against the caps.
func TestOIDCTTLStore_RePutReplaces(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingCode](2)
	now := time.Now()
	store.put("a", "o", 2, oidcPendingCode{UserID: "1"}, now, time.Minute)
	store.put("b", "o", 2, oidcPendingCode{UserID: "2"}, now, time.Minute)
	res := store.put("a", "o", 2, oidcPendingCode{UserID: "3"}, now, time.Minute)
	if res.OwnerEvicted != 0 || res.GlobalEvicted != 0 {
		t.Fatalf("re-put evicted %+v", res)
	}
	if v, _ := store.peek("a", now); v.UserID != "3" {
		t.Fatalf("re-put must replace the value, got %q", v.UserID)
	}
	if _, ok := store.peek("b", now); !ok {
		t.Fatal("re-put must not evict another entry")
	}
}

// The rate-limit key ignores Cf-Connecting-Ip and X-Forwarded-For: the origin
// cannot tell a header Cloudflare set from one a direct caller forged. It uses
// gin's ClientIP under the router's trusted-proxy list, configured here exactly
// as server.go configures production (SetTrustedProxies(nil)).
func TestOIDCRateKey_IgnoresSpoofableForwardedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, engine := gin.CreateTestContext(httptest.NewRecorder())
	if err := engine.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	c.Request = httptest.NewRequest("GET", "/auth/openid", nil)
	base := oidcRateKey(c, "svc:family")
	c.Request.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	c.Request.Header.Set("X-Forwarded-For", "203.0.113.10")
	if got := oidcRateKey(c, "svc:family"); got != base {
		t.Fatalf("rate key changed with forged headers: %q vs %q", got, base)
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

// Everything on the consent page is escaped. The account name comes from the IdP
// and the scheme from configuration; neither may inject markup.
func TestRenderOIDCConsent_EscapesEverything(t *testing.T) {
	t.Setenv(OIDCRedirectURIsEnvVar, "")
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", OIDCBrowserPath, nil)
	h := &Handler{oidcConsentKey: []byte("test-key")}
	hostile := `"><script>alert(1)</script>@example.com`
	h.renderOIDCConsent(c, "REQ", oidcPendingRequest{RedirectURI: `x<img src=y>:cb`},
		&servermiddleware.ABSIdentity{User: &database.User{ID: "u1", Username: "u"}, Email: hostile})
	body := w.Body.String()
	// (A bare `"><` probe is useless: the template's own <html lang="en"><head>
	// contains it.)
	for _, raw := range []string{"<script>", "<img", `"><script`} {
		if strings.Contains(body, raw) {
			t.Fatalf("consent page contains unescaped %q:\n%s", raw, body)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("the account should be rendered escaped:\n%s", body)
	}
}

// A cap below 1 is raised to 1. With max 0, put's global eviction loop would
// spin forever on an empty map.
func TestOIDCTTLStore_MaxBelowOneIsClamped(t *testing.T) {
	for _, limit := range []int{0, -5} {
		store := newOIDCTTLStore[oidcPendingCode](limit)
		now := time.Now()
		store.put("a", "o", 1, oidcPendingCode{}, now, time.Minute)
		store.put("b", "p", 1, oidcPendingCode{}, now, time.Minute)
		if len(store.entries) != 1 {
			t.Fatalf("max %d: store holds %d entries, want 1", limit, len(store.entries))
		}
		if _, ok := store.peek("b", now); !ok {
			t.Fatalf("max %d: the newest put must land", limit)
		}
	}
}

// Every put sweeps expired entries BEFORE the per-owner eviction, so an
// owner's live entry is never evicted while its own expired entry still holds
// a slot. Here the expired entry is the owner's newer one (a different TTL on
// the same owner), so eviction-by-age alone would pick the live one.
func TestOIDCTTLStore_SweepsBeforeOwnerEviction(t *testing.T) {
	store := newOIDCTTLStore[oidcPendingCode](100)
	now := time.Now()
	store.put("live", "o", 2, oidcPendingCode{}, now, time.Hour)
	store.put("short", "o", 2, oidcPendingCode{}, now, time.Second)
	later := now.Add(2 * time.Second)
	res := store.put("new", "o", 2, oidcPendingCode{}, later, time.Hour)
	if res.OwnerEvicted != 0 {
		t.Fatalf("OwnerEvicted = %d, want 0: the expired entry should have been swept instead", res.OwnerEvicted)
	}
	for _, k := range []string{"live", "new"} {
		if _, ok := store.peek(k, later); !ok {
			t.Fatalf("%s must still be live", k)
		}
	}
}

// Concurrent puts, takes and peeks across owners keep both caps and never
// lose a fresh put to a data race. Meaningful under -race: every path takes
// the store's one lock, and this proves no path skips it.
func TestOIDCTTLStore_ConcurrentPutsKeepInvariants(t *testing.T) {
	const limit, ownerCap, workers, perWorker = 64, 5, 16, 300
	store := newOIDCTTLStore[oidcPendingCode](limit)
	now := time.Now()
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			owner := fmt.Sprintf("o%d", w%6)
			for i := range perWorker {
				key := fmt.Sprintf("w%d-%d", w, i)
				store.put(key, owner, ownerCap, oidcPendingCode{UserID: owner}, now, time.Minute)
				if i%3 == 0 {
					store.take(key, now)
				} else {
					store.peek(fmt.Sprintf("w%d-%d", (w+1)%workers, i), now)
				}
			}
		})
	}
	wg.Wait()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.entries) > limit {
		t.Fatalf("store holds %d entries, cap %d", len(store.entries), limit)
	}
	per := map[string]int{}
	for _, e := range store.entries {
		per[e.owner]++
		if per[e.owner] > ownerCap {
			t.Fatalf("owner %s holds %d entries, cap %d", e.owner, per[e.owner], ownerCap)
		}
	}
}

// mintOIDCCode refuses a challenge that is not S256-shaped: such a code could
// never be redeemed, so minting it only hides a caller's mistake.
func TestMintOIDCCode_RequiresS256ShapedChallenge(t *testing.T) {
	t.Setenv(OIDCRedirectURIsEnvVar, "")
	IsolateOIDCCodesForTest(t)
	_, good := pkcePair("a-verifier-a-verifier-a-verifier-a-verifier")
	for _, bad := range []string{"", "short", good + "x", strings.Repeat("!", 43)} {
		if _, err := mintOIDCCode(&database.User{ID: "u1"}, "e@example.com", bad, "audiobooth://oauth", time.Now()); err == nil {
			t.Fatalf("challenge %q must be refused", bad)
		}
	}
	if _, err := mintOIDCCode(&database.User{ID: "u1"}, "e@example.com", good, "audiobooth://oauth", time.Now()); err != nil {
		t.Fatalf("a real S256 challenge must mint: %v", err)
	}
}

// Every origin compared in this file goes through one normaliser, so equal
// origins are equal strings — scheme, host and port included.
func TestOIDCNormalizeOrigin(t *testing.T) {
	cases := []struct {
		raw       string
		allowPath bool
		want      string // "" = refused
	}{
		{"https://example.com", false, "https://example.com"},
		{"https://Example.COM:443", false, "https://example.com"},
		{"HTTPS://example.com", false, "https://example.com"},
		{"https://example.com:8443", false, "https://example.com:8443"},
		{"https://[::1]:443", false, "https://[::1]"},
		{"https://[::1]:8443", false, "https://[::1]:8443"},
		{"http://example.com", false, ""},
		{"null", false, ""},
		{"", false, ""},
		{"https://", false, ""},
		{"https://u:p@example.com", false, ""},
		{"https://example.com/", false, ""},
		{"https://example.com?x=1", false, ""},
		{"https://example.com#f", false, ""},
		{"https://example.com:notaport", false, ""},
		{"https://example.com/abs/", true, "https://example.com"},
		{"https://Books.Example.net:443/x", true, "https://books.example.net"},
		{"http://example.com/abs", true, ""},
		{"example.com", true, ""},
	}
	for _, tc := range cases {
		got, ok := oidcNormalizeOrigin(tc.raw, tc.allowPath)
		if tc.want == "" {
			if ok {
				t.Errorf("oidcNormalizeOrigin(%q, %t) = %q, want refused", tc.raw, tc.allowPath, got)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("oidcNormalizeOrigin(%q, %t) = %q, %t; want %q", tc.raw, tc.allowPath, got, ok, tc.want)
		}
	}
	for host, want := range map[string]string{
		"example.com": "https://example.com", "Example.com:443": "https://example.com",
		"evil.example/@x": "", "a b": "", "": "", "x%2f": "",
	} {
		got, ok := oidcHostOrigin(host)
		if (want == "" && ok) || (want != "" && got != want) {
			t.Errorf("oidcHostOrigin(%q) = %q, %t; want %q", host, got, ok, want)
		}
	}
}

// The hop-2 load log line's assertion kind is what tells the two
// strict_service_token_auth behaviours apart, so each kind is pinned.
func TestOIDCAssertionKind(t *testing.T) {
	gin.SetMode(gin.TestMode)
	person := &servermiddleware.ABSIdentity{User: &database.User{ID: "u1"}}
	mk := func(header, serviceToken string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", OIDCBrowserPath, nil)
		if header != "" {
			c.Request.Header.Set("Cf-Access-Jwt-Assertion", header)
		}
		if serviceToken != "" {
			c.Set("abs_service_token", serviceToken)
		}
		return c
	}
	cases := []struct {
		name     string
		c        *gin.Context
		identity *servermiddleware.ABSIdentity
		authErr  *servermiddleware.ABSAuthError
		want     string
	}{
		{"no header", mk("", ""), nil, nil, oidcAssertionNone},
		{"rejected", mk("x", ""), nil, &servermiddleware.ABSAuthError{Status: 401}, oidcAssertionRejected},
		{"person", mk("x", ""), person, nil, oidcAssertionPerson},
		{"service token", mk("x", "family"), nil, nil, oidcAssertionServiceToken},
		{"no person, no token", mk("x", ""), nil, nil, oidcAssertionNoPerson},
	}
	for _, tc := range cases {
		if got := oidcAssertionKind(tc.c, tc.identity, tc.authErr); got != tc.want {
			t.Errorf("%s: kind = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Cookie names only, CF_ prefix in any case, never a value.
func TestOIDCCFCookieNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", OIDCBrowserPath, nil)
	if got := oidcCFCookieNames(c); got != "-" {
		t.Fatalf("no cookies: %q, want -", got)
	}
	c.Request.Header.Set("Cookie", "CF_Authorization=secret-jwt; session=abc; cf_clearance=zzz")
	got := oidcCFCookieNames(c)
	if got != "CF_Authorization,cf_clearance" {
		t.Fatalf("names = %q", got)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "zzz") {
		t.Fatalf("a cookie value leaked into %q", got)
	}
}
