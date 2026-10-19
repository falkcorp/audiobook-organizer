// file: internal/server/handlers/abs/openid.go
// version: 2.7.0
// guid: 3b8e5a14-70c9-4f26-9d51-a2c60f7b8e93
// last-edited: 2026-10-19

package abs

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Single sign-on for Audiobookshelf clients, implemented against AudioBooth's
// actual source rather than a guess about it
// (github.com/AudioBooth/AudioBooth, MPL-2.0).
//
// # What the client does
//
// OIDCAuthenticationManager.swift:
//
//  1. Makes the FIRST request itself, from an ephemeral URLSession that does not
//     follow redirects and carries the app's Custom Headers (a Cloudflare service
//     token):
//     GET /auth/openid?client_id=AudioBooth&response_type=code&scope=openid
//     &redirect_uri=audiobooth://oauth&callback=audiobooth://oauth
//     &code_challenge=<S256>&code_challenge_method=S256
//     It sends no `state`. It expects a 302, or a 400 whose plain-text body it
//     shows to the user ("Invalid redirect_uri" gets a dedicated hint); any other
//     status is a generic "bad server response".
//  2. Hands that 302's Location to ASWebAuthenticationSession.authenticate(using:),
//     which opens it in the system browser and waits for a navigation to the
//     `audiobooth` scheme. The Location MUST be an absolute http(s) URL: a custom
//     scheme URL here crashes the app. On iOS 17.4+ the Custom Headers are also
//     set on that browser load — on the INITIAL URL only, per Apple's docs.
//  3. AuthenticationService.loginWithOIDC then calls
//     GET /auth/openid/callback?code=…&code_verifier=…[&state=…]
//     from the app's own URLSession, and decodes the reply as Authorize.
//
// Real Audiobookshelf answers step 1 with a redirect to its identity provider.
// Until 2026-10-19 this file answered it with a redirect straight to
// audiobooth://oauth (a code on success, an error on failure), on the false
// assumption that step 1 came from the system browser carrying the person's
// Cloudflare Access cookie. It does not: step 1 carries only the service token,
// so it resolved no person, and the resulting custom-scheme Location crashed the
// app — reproduced in production.
//
// # The flow now: two hops
//
//	hop 1  GET  /auth/openid           (app's URLSession, service token only)
//	       require a verified Cloudflare credential, rate-limit and cap live
//	       requests per credential, validate redirect_uri + PKCE, park the
//	       request under a random id,
//	       302 → https://<origin>/auth/openid/browser?req=<id>&state=<id>
//	             &client_id=…&scope=openid&redirect_uri=<allow-listed uri>
//	       errors: 400 text/plain, never a redirect
//
//	hop 2  GET  /auth/openid/browser   (system browser — Cloudflare Access
//	                                    authenticates the PERSON here)
//	       resolve the person and render a consent page naming the app, the
//	       callback scheme and the account. Side-effect free: no code is minted
//	       and the parked request is not consumed.
//	       errors: 302 → <redirect_uri>?error=access_denied&state=…
//	       (safe here: ASWebAuthenticationSession is waiting for exactly that)
//
//	       POST /auth/openid/browser   (the consent form's Allow / Deny)
//	       re-resolve the person, check the CSRF token bound to (req id, user),
//	       consume the parked request, mint a code bound to its challenge and
//	       redirect_uri, 303 → <redirect_uri>?code=…&state=…
//
//	hop 3  GET  /auth/openid/callback  (app's URLSession, service token only)
//	       redeem code + code_verifier → Authorize JSON via issueSession
//
// Hop 1 resolves no identity at all, even when a person assertion is present,
// and never redirects to a custom scheme. It is the hop the app inspects with
// its own HTTP client, so the only thing it may ever hand back is an https URL.
//
// # The official Audiobookshelf app
//
// Real ABS answers hop 1 with its identity provider's authorize URL, and the
// official app (audiobookshelf-app ServerConnectForm.vue, clickLoginWithOpenId)
// does not open that Location as given: it requires client_id, scope, state
// and redirect_uri in it, then REBUILDS the URL from host + pathname plus
// those four (and its own code_challenge), dropping every other parameter, and
// later refuses a callback whose state differs from the Location's. So hop 1's
// Location carries all four, with the request id as `state`, and hop 2 accepts
// the id from `state` when `req` is absent. AudioBooth opens the Location
// verbatim and uses `req`. Either way hop 2 ignores everything else in its
// query: client_id, scope and redirect_uri are echoes for the official app's
// presence check, and redirect_uri and state are taken from the parked
// request.
//
// The callback's state is the client's own state when it sent one. When it
// sent none — neither app does — it is the request id, which is the state the
// official app saw in the Location and will compare against. The id is echoed
// only after the consent POST has consumed the request; an error from the
// hop-2 GET leaves the request parked, so that redirect carries only the
// client's own state (the official app reads `error` before comparing state).
// audiobookshelf://oauth is not on the default redirect allowlist; an
// operator adds it with ABS_OIDC_REDIRECT_URIS.
//
// # Why there is a consent page
//
// Nothing the two hops share can tie the browser to the device that started the
// flow: hop 1 is the app's ephemeral URLSession and hop 2 is the system browser,
// so a cookie set on hop 1 never reaches hop 2; AudioBooth sends no state; and
// the client IP is useless (shared reverse proxies in front of the origin, and
// iCloud Private Relay in front of Safari). So anyone who can start hop 1 can
// send the hop-2 link to someone else, and a GET that minted on sight would
// issue a code for whoever clicked it. The code is bound to the starter's PKCE
// challenge and is only ever delivered to an allow-listed redirect_uri, so the
// starter still needs that redirect to reach them; but minting for a person who
// never asked is wrong regardless. The consent page makes minting require the
// person's explicit Allow, on a POST that carries a token only this server can
// produce for that request id and that person.
//
// # The browser hop and the service token
//
// On iOS 17.4+ ASWebAuthenticationSession sets the app's Custom Headers — the
// Cloudflare service token — on the browser's INITIAL load of hop 2, and only
// on that load. What Cloudflare does with a token-bearing browser load depends
// on the Zero Trust organisation's strict_service_token_auth setting, which
// could not be read when this was written (2026-10-19: no Cloudflare API token
// on any host checked), so both behaviours are handled and logged:
//
//   - Strict mode ON: Access judges a request that carries a service token
//     only against Service Auth policies, ignoring Allow policies and any
//     CF_Authorization cookie. The Access application covering
//     /auth/openid/browser must therefore include the app's Service Auth
//     policy, or Cloudflare denies the first load at the edge — the origin
//     never sees it and the person gets a Cloudflare error page inside the
//     sign-in sheet. With the policy in place the first load arrives as a
//     person-less service-token assertion; hop 2 redirects ONCE to itself with
//     reauth=1, that reload is not the initial URL and so carries no token
//     header, and Access has to authenticate the person (their Access cookie
//     or a fresh IdP login).
//   - Strict mode OFF: Access may admit the token-bearing first load and set a
//     CF_Authorization cookie for the SERVICE TOKEN. AudioBooth uses the shared
//     browser session, so that cookie can land in Safari's shared jar and ride
//     along on the reauth load, which Access then admits on the token again.
//     The bounce does not shed it, and the sign-in ends in access_denied.
//
// Every hop-2 load logs the assertion kind it received and the names of any CF_
// cookies, and a reauth load that still carries a service token gets its own
// warning, so the journal says which case is live. Only after the single
// bounce does a missing identity become a terminal access_denied.
//
// # Why hop 3 does not require an identity assertion
//
// The callback is the app's URLSession, which does not share the browser's
// cookie jar. The app reaches the origin with its service token — a MACHINE
// identity with no email. The authorization code is what carries the human
// identity across that gap. That is ordinary OAuth, and it is why the callback
// is authenticated by the code plus its PKCE verifier and nothing else.
const (
	// oidcCodeTTL bounds how long a minted code is redeemable. AudioBooth
	// exchanges within a second of the redirect; a minute is generous and keeps
	// a leaked redirect URL from being useful for long.
	oidcCodeTTL = 60 * time.Second

	// oidcRequestTTL bounds how long a parked hop-1 request waits for the
	// browser. It has to cover an IdP sign-in (password, 2FA) plus the consent
	// tap, and no more: a shorter life is less time for a flood to hold slots.
	oidcRequestTTL = 5 * time.Minute

	// oidcStoreCap is the most entries either in-memory store holds. See
	// oidcTTLStore for the full set of guarantees.
	oidcStoreCap = 1024

	// Per-owner caps. An owner is the verified Cloudflare credential that
	// started hop 1 (or, for codes, the user the code was minted for).
	//
	// A service token is shared by a whole GROUP of people and devices (the
	// owner mints one per friends/family/other/testing group), so its cap must
	// cover a group's concurrent sign-ins: 256 is far above any real group and
	// is oidcStoreCap/4, so the four group tokens together fit exactly. A
	// person (email) or a user's codes need only a few.
	oidcSharedCredentialCap = 256
	oidcPersonCap           = 3

	// Hop-1 rate limits per verified credential and client address (see
	// oidcRateKey): generous for a shared group token, tight for one person.
	oidcSharedRatePerMinute = 60
	oidcSharedRateBurst     = 30
	oidcPersonRatePerMinute = 10
	oidcPersonRateBurst     = 5

	// oidcAuthMethod labels this path in audit records and ABSIdentity.Mode.
	oidcAuthMethod = "openid"

	// OIDCBrowserPath is hop 2, opened in the system browser.
	OIDCBrowserPath = "/auth/openid/browser"

	// oidcReauthParam marks hop 2's single self-redirect (see the file comment).
	oidcReauthParam = "reauth"

	// oidcMaxClientIDLen caps client_id. Real clients send a short fixed name
	// ("AudioBooth", "Audiobookshelf-App"); the value is parked, echoed into the
	// hop-2 URL and logged, so it is bounded like state rather than trusted.
	oidcMaxClientIDLen = 128

	// oidcMaxLogValueLen caps any caller-supplied string written to a log line.
	oidcMaxLogValueLen = 128

	// oidcMaxStateLen caps the client's opaque state. It is echoed back in a URL,
	// so an unbounded value would let hop 1 park arbitrary amounts of memory.
	oidcMaxStateLen = 512

	// oidcS256ChallengeLen is the length of BASE64URL(SHA256(x)) without
	// padding. Every valid S256 challenge has exactly this length, and so does
	// every id absauth.NewRefreshSeed returns (32 random bytes).
	oidcS256ChallengeLen = 43
)

var oidcLog = logger.New("abs")

// oidcTTLStore is a bounded, single-use, expiring map. It backs both the parked
// hop-1 requests and the minted authorization codes.
//
// Everything lives in memory: a restart invalidates every in-flight sign-in,
// which is correct for credentials that live seconds to minutes, and keeps
// short-lived secrets out of the database.
//
// INVARIANTS (enforced on every put; put always succeeds):
//
//  1. Global cap: the store never holds more than max entries.
//  2. Per-owner cap: an owner never holds more than the cap passed with its
//     put; at the cap, a new entry evicts that owner's OWN oldest.
//  3. A flood evicts only itself: at the global cap, the entry evicted is the
//     oldest entry of the owner holding the MOST live entries — never simply
//     the globally oldest — so an owner flooding the store pushes out its own
//     entries first, and anyone below the largest owner's count is untouched.
//  4. Expired entries are swept before anything live is evicted: every put
//     sweeps first, so neither eviction loop can pick a live entry while an
//     expired one is still holding a slot. (Within one store every entry gets
//     the same TTL, so oldest-by-seq would usually be earliest-expiry anyway,
//     but that rests on the clock never stepping back; sweeping does not.)
//  5. take is single-use: the read and the delete happen under one lock.
//
// The scans are O(n) with n ≤ max (1024), on a path that runs a handful of times
// an hour and is rate-limited per caller, so a heap or a per-owner index would
// be complexity without a payoff.
type oidcTTLStore[V any] struct {
	mu      sync.Mutex
	entries map[string]*oidcTTLEntry[V]
	max     int
	seq     uint64
}

type oidcTTLEntry[V any] struct {
	value     V
	owner     string
	expiresAt time.Time
	seq       uint64 // insertion order; lower is older
}

// oidcPutResult reports what a put had to evict to make room.
type oidcPutResult struct {
	OwnerEvicted  int    // the owner's own oldest entries, over its cap
	GlobalEvicted int    // entries evicted at the global cap
	EvictedFrom   string // the owner the global eviction took from
}

// newOIDCTTLStore returns an empty store holding at most maxEntries. A cap
// below 1 is raised to 1: with max 0 the global eviction loop in put would spin
// on an empty map, and a store that can hold nothing is never what was meant.
func newOIDCTTLStore[V any](maxEntries int) *oidcTTLStore[V] {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &oidcTTLStore[V]{entries: map[string]*oidcTTLEntry[V]{}, max: maxEntries}
}

// put registers key, owned by owner (capped at ownerCap live entries), until
// now+ttl. It always succeeds; see the type comment for what it evicts.
func (s *oidcTTLStore[V]) put(key, owner string, ownerCap int, value V, now time.Time, ttl time.Duration) oidcPutResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res oidcPutResult
	delete(s.entries, key) // re-putting a key replaces it and frees its slot
	for k, e := range s.entries {
		if !now.Before(e.expiresAt) {
			delete(s.entries, k)
		}
	}
	if ownerCap < 1 {
		ownerCap = 1
	}
	for s.countOwnerLocked(owner) >= ownerCap {
		s.evictOldestOfLocked(owner)
		res.OwnerEvicted++
	}
	for len(s.entries) >= s.max {
		victim := s.largestOwnerLocked()
		s.evictOldestOfLocked(victim)
		res.GlobalEvicted++
		res.EvictedFrom = victim
	}
	s.seq++
	s.entries[key] = &oidcTTLEntry[V]{value: value, owner: owner, expiresAt: now.Add(ttl), seq: s.seq}
	return res
}

func (s *oidcTTLStore[V]) countOwnerLocked(owner string) int {
	n := 0
	for _, e := range s.entries {
		if e.owner == owner {
			n++
		}
	}
	return n
}

// largestOwnerLocked returns the owner with the most live entries; a tie goes to
// the owner holding the older entry, so the choice is deterministic.
func (s *oidcTTLStore[V]) largestOwnerLocked() string {
	count := map[string]int{}
	oldest := map[string]uint64{}
	for _, e := range s.entries {
		count[e.owner]++
		if o, ok := oldest[e.owner]; !ok || e.seq < o {
			oldest[e.owner] = e.seq
		}
	}
	best, bestN, bestSeq := "", -1, uint64(0)
	for o, n := range count {
		if n > bestN || (n == bestN && oldest[o] < bestSeq) {
			best, bestN, bestSeq = o, n, oldest[o]
		}
	}
	return best
}

// evictOldestOfLocked removes owner's oldest entry.
func (s *oidcTTLStore[V]) evictOldestOfLocked(owner string) {
	oldestKey, oldestSeq, found := "", uint64(0), false
	for k, e := range s.entries {
		if e.owner != owner {
			continue
		}
		if !found || e.seq < oldestSeq {
			oldestKey, oldestSeq, found = k, e.seq, true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
}

// take returns the entry for key and removes it. It reports false for a key
// that is unknown, already taken, evicted, or expired at now.
//
// SINGLE USE is enforced here rather than at the call site: the delete happens
// under the same lock as the read, so two concurrent redemptions of one key
// cannot both succeed. An expired entry is deleted too, so it cannot be retried.
func (s *oidcTTLStore[V]) take(key string, now time.Time) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	delete(s.entries, key)
	if !now.Before(e.expiresAt) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// peek returns the entry for key without consuming it, or false when it is
// unknown, taken, evicted or expired.
func (s *oidcTTLStore[V]) peek(key string, now time.Time) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok || !now.Before(e.expiresAt) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// oidcPendingCode is one minted authorization code awaiting redemption.
type oidcPendingCode struct {
	UserID      string
	Email       string
	Challenge   string
	RedirectURI string
	// CredentialEpoch is the user's User.CredentialEpoch when the code was
	// minted. A password change, reset or status change in between moves it, and
	// the callback then refuses the code rather than mint a session at the new
	// epoch from a sign-in that predates the change.
	CredentialEpoch int64
}

// oidcPendingRequest is one hop-1 request parked until the browser arrives.
// redirect_uri and state are taken from HERE at hop 2, never from hop 2's own
// query string, so the browser hop cannot be steered somewhere else.
type oidcPendingRequest struct {
	Challenge   string
	RedirectURI string
	State       string
	ClientID    string
	// Hop 1's verified Cloudflare credential key (svc:<token> or email:<…>):
	// the entry's owner in the store, and on the hop-2 audit line.
	CallerKey string
	// Hop 1's client address, recorded for the audit line. Not enforced: Safari
	// with iCloud Private Relay legitimately reaches us from a different address
	// than the app's own URLSession, and behind the shared proxies every caller
	// has the same address anyway. (The User-Agent is not parked: it is
	// caller-chosen, unbounded, and nothing reads it back.)
	SourceIP string
}

// oidcCodes is package-level because the web OAuth deep-link flow
// (internal/server/handlers) mints into it through MintAppAuthorizationCode.
// Codes are owned by the user they were minted for.
var oidcCodes = newOIDCTTLStore[oidcPendingCode](oidcStoreCap)

// OIDCRedirectURIsEnvVar overrides the redirect_uri allowlist (comma-separated,
// exact matches). Set it when adding a client whose callback scheme differs.
const OIDCRedirectURIsEnvVar = "ABS_OIDC_REDIRECT_URIS"

// defaultOIDCRedirectURIs are the callback URIs shipped clients register.
var defaultOIDCRedirectURIs = []string{"audiobooth://oauth"}

// oidcRedirectAllowed reports whether a redirect_uri may receive an
// authorization code.
//
// THIS IS THE CONTROL THAT PREVENTS ACCOUNT TAKEOVER, and it cannot be replaced
// by PKCE. An unvalidated redirect_uri lets an attacker send:
//
//	/auth/openid?redirect_uri=https://evil.example
//	            &code_challenge=<THEIR OWN>&code_challenge_method=S256
//
// to a signed-in victim. The code is minted for the VICTIM and delivered to the
// attacker, who supplied the challenge and therefore holds the verifier — so
// they redeem it and get the victim's session. PKCE binds the code to whoever
// chose the challenge, which in that flow is the attacker.
//
// Matching is exact, not prefix or host-based: a prefix rule is defeated by
// "audiobooth://oauth.evil.example" and a host rule by userinfo tricks. There is
// no legitimate need for anything but a handful of fixed strings.
func oidcRedirectAllowed(uri string) bool {
	allowed := defaultOIDCRedirectURIs
	if raw := strings.TrimSpace(os.Getenv(OIDCRedirectURIsEnvVar)); raw != "" {
		allowed = nil
		for part := range strings.SplitSeq(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				allowed = append(allowed, p)
			}
		}
	}
	for _, a := range allowed {
		if subtle.ConstantTimeCompare([]byte(a), []byte(uri)) == 1 {
			return true
		}
	}
	return false
}

// ── seam for the web OAuth deep-link flow (internal/server/handlers) ────────
//
// The web provider flow (/auth/oauth/:provider/start) can hand a native client back
// to its own URL scheme instead of the SPA root. It resolves identity through
// Google/GitHub rather than through a Cloudflare Access assertion, but the last hop
// is IDENTICAL: mint a single-use PKCE-bound code here and let the client redeem it
// at /auth/openid/callback. These functions are the only way in.
//
// They are exported rather than the code store itself so there is exactly one place
// that decides what a valid app callback is, and so no caller can put a code into
// the store without a challenge and a redirect_uri to bind it to.

// AppRedirectURIAllowed reports whether uri is a registered native-client callback.
//
// It is the SAME exact-match allowlist /auth/openid enforces (see
// oidcRedirectAllowed): this is the control that prevents account takeover, and a
// second flow reaching the same code store must not get a second, looser opinion
// about which targets are legitimate.
func AppRedirectURIAllowed(uri string) bool { return oidcRedirectAllowed(uri) }

// MintAppAuthorizationCode issues a single-use authorization code bound to challenge
// (the CLIENT's PKCE challenge — never the server's own IdP verifier), to
// redirectURI, and to the resolved user. It expires in oidcCodeTTL and is
// redeemable exactly once, at /auth/openid/callback.
//
// challenge is required, and must have the S256 shape (43 base64url characters).
// A code minted without one would be a bearer credential
// sitting in a URL on a custom scheme that any other installed app can register —
// which is precisely the takeover PKCE exists to stop — so this refuses rather than
// defaulting to an unbound code. redirectURI must be on the allowlist, re-checked
// here so no caller can bind a code to a target the allowlist never approved.
//
// user is the record the caller just resolved; the code carries its
// CredentialEpoch (see oidcPendingCode).
func MintAppAuthorizationCode(user *database.User, email, challenge, redirectURI string) (string, error) {
	return mintOIDCCode(user, email, challenge, redirectURI, time.Now())
}

func mintOIDCCode(user *database.User, email, challenge, redirectURI string, now time.Time) (string, error) {
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return "", errors.New("abs: cannot mint an authorization code with no user")
	}
	if !isS256Shaped(challenge) {
		// Not merely non-empty: a challenge that is not 43 base64url characters
		// can never equal BASE64URL(SHA256(verifier)), so such a code could never
		// be redeemed. Refusing here keeps every caller of this seam honest about
		// passing the client's S256 challenge rather than something else.
		return "", errors.New("abs: cannot mint an authorization code without an S256 PKCE challenge")
	}
	if !oidcRedirectAllowed(redirectURI) {
		return "", errors.New("abs: cannot mint an authorization code for an unregistered redirect_uri")
	}
	code, err := absauth.NewRefreshSeed() // 256-bit URL-safe random; not a refresh token
	if err != nil {
		return "", err
	}
	oidcCodes.put(code, user.ID, oidcPersonCap, oidcPendingCode{
		UserID:          user.ID,
		Email:           email,
		Challenge:       challenge,
		RedirectURI:     redirectURI,
		CredentialEpoch: user.CredentialEpoch,
	}, now, oidcCodeTTL)
	return code, nil
}

// AppRedirectURL assembles the callback URL a native client is sent back to. Empty
// params are skipped, and the custom scheme survives verbatim.
func AppRedirectURL(target, code, state string) string {
	return buildOIDCRedirect(target, map[string]string{"code": code, "state": state})
}

// OpenIDAuthorize handles GET /auth/openid — hop 1, called by the app's own HTTP
// client, which does not follow redirects and carries only a service token.
//
// It resolves no identity. It requires a VERIFIED Cloudflare credential (the
// app's service token, or a person's assertion), rate-limits and caps live
// parked requests per that credential, validates redirect_uri and PKCE, parks
// the request under a random single-use id, and 302s to hop 2 on this server's
// own https origin.
//
// Every failure is a 400 with a plain-text body and no Location: the app shows a
// 400 body to the user and treats any other status as a generic error, and it
// hands any Location it receives to ASWebAuthenticationSession, which crashes on
// anything but an http(s) URL. So this hop never redirects to the client's
// custom scheme, on success or on failure.
func (h *Handler) OpenIDAuthorize(c *gin.Context) {
	oidcNoStore(c)

	// AudioBooth sends both; redirect_uri is the OAuth-standard name and wins.
	redirectURI := strings.TrimSpace(c.Query("redirect_uri"))
	if redirectURI == "" {
		redirectURI = strings.TrimSpace(c.Query("callback"))
	}
	state := c.Query("state")
	clientID := c.Query("client_id")

	if redirectURI == "" {
		h.oidcAuthorizeReject(c, "redirect-uri-missing", "redirect_uri is required")
		return
	}
	// "Invalid redirect_uri" is real ABS's wording, and AudioBooth matches it
	// exactly to show its "add audiobooth://oauth to your allowed redirect URIs"
	// hint. The target is never navigated to: reporting a failure by bouncing to
	// an unvalidated URI would itself be an open redirect.
	if !oidcRedirectAllowed(redirectURI) {
		oidcLog.Warn("openid authorize rejected an unregistered redirect_uri: redirect_uri=%s source_ip=%s",
			oidcLogValue(redirectURI), logger.SanitizeLogValue(strings.TrimSpace(c.ClientIP())))
		h.oidcAuthorizeReject(c, "redirect-uri-not-registered", "Invalid redirect_uri")
		return
	}
	// Single sign-on completes only on a verified Cloudflare Access identity at
	// hop 2. With ABS_AUTH_MODES lacking cf, or no verifier configured, no
	// browser could ever finish, so say so now instead of sending the person
	// through a browser sign-in that is certain to fail.
	if !h.resolver.CFAssertionsEnabled() {
		h.oidcAuthorizeReject(c, "sso-unavailable",
			"Single sign-on is not available on this server. Sign in with a username and password.")
		return
	}
	// The verified credential is the quota and rate-limit key. c.ClientIP() is
	// not usable: the origin trusts no forwarded headers, and behind the shared
	// reverse proxies every caller has the same address, so a per-IP quota would
	// let one caller lock out everyone. A request with no verified credential
	// never came through Cloudflare Access, and such a flow could not complete
	// at hop 2 anyway, so it is refused before it can occupy a slot.
	callerKey, ok := h.resolver.CFCallerKey(c)
	if !ok {
		h.oidcAuthorizeReject(c, "no-verified-credential",
			"Single sign-on must go through Cloudflare Access. Check the app's custom headers.")
		return
	}
	shared := strings.HasPrefix(callerKey, "svc:")
	limiter, ownerCap := h.oidcPersonLimiter, oidcPersonCap
	if shared {
		limiter, ownerCap = h.oidcSharedLimiter, oidcSharedCredentialCap
	}
	if !limiter.AllowKey(oidcRateKey(c, callerKey)) {
		oidcLog.Warn("openid authorize rate-limited: caller=%s", logger.SanitizeLogValue(callerKey))
		h.oidcAuthorizeReject(c, "rate-limited", "Too many sign-in attempts. Wait a minute and try again.")
		return
	}
	// PKCE is mandatory, and only S256 is accepted. "plain" would let anyone who
	// observes the redirect redeem the code, which on a custom URL scheme is a
	// realistic threat: another installed app can register the same scheme.
	challenge := strings.TrimSpace(c.Query("code_challenge"))
	if !strings.EqualFold(strings.TrimSpace(c.Query("code_challenge_method")), "S256") || !isS256Shaped(challenge) {
		h.oidcAuthorizeReject(c, "pkce-invalid",
			"code_challenge (43 base64url characters) with code_challenge_method=S256 is required")
		return
	}
	if len(state) > oidcMaxStateLen {
		h.oidcAuthorizeReject(c, "state-too-long", "state is too long")
		return
	}
	if len(clientID) > oidcMaxClientIDLen {
		h.oidcAuthorizeReject(c, "client-id-too-long", "client_id is too long")
		return
	}
	origin, ok := h.oidcOrigin(c)
	if !ok {
		h.oidcAuthorizeReject(c, "host-invalid", "invalid Host header")
		return
	}
	if hostOrigin, ok := oidcHostOrigin(c.Request.Host); ok && hostOrigin != origin {
		// A configured public URL is in use and the app reached a different
		// hostname. Fine if both are fronted by the same Access application;
		// if not, hop 2 will fail its assertion check (see oidcOrigin).
		oidcLog.Warn("openid authorize: browser hop goes to the configured origin %s, but the app reached %s",
			logger.SanitizeLogValue(origin), logger.SanitizeLogValue(hostOrigin))
	}

	id, err := absauth.NewRefreshSeed() // 256-bit URL-safe random
	if err != nil {
		oidcLog.Error("openid authorize could not generate a request id: %v", err)
		h.oidcAuthorizeReject(c, "request-id-error", "Could not start sign-in. Try again.")
		return
	}
	res := h.oidcRequests.put(id, callerKey, ownerCap, oidcPendingRequest{
		Challenge:   challenge,
		RedirectURI: redirectURI,
		State:       state,
		ClientID:    clientID,
		CallerKey:   callerKey,
		SourceIP:    strings.TrimSpace(c.ClientIP()),
	}, h.now(), oidcRequestTTL)
	if res.GlobalEvicted > 0 {
		// Only reachable when many distinct credentials are parking requests at
		// once: a flood signal worth a line.
		oidcLog.Warn("openid authorize evicted %d parked request(s) of caller=%s at the global cap %d (new caller=%s)",
			res.GlobalEvicted, logger.SanitizeLogValue(res.EvictedFrom), oidcStoreCap, logger.SanitizeLogValue(callerKey))
	}

	absauth.Audit(absauth.AuditEvent{
		Action: "openid-authorize", Outcome: absauth.OutcomeSuccess, Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), Reason: "browser-hop-issued",
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		ServiceToken: servermiddleware.ABSServiceToken(c),
	})
	c.Header("Location", oidcHop1Location(origin, id, clientID, redirectURI))
	c.Status(http.StatusFound)
}

// oidcAuthorizeReject answers hop 1 with a 400 plain-text body and audits it.
func (h *Handler) oidcAuthorizeReject(c *gin.Context, reason, message string) {
	absauth.Audit(absauth.AuditEvent{
		Action: "openid-authorize", Outcome: absauth.OutcomeDenied, Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), Reason: reason,
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		ServiceToken: servermiddleware.ABSServiceToken(c),
	})
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusBadRequest, message)
	c.Abort()
}

// OpenIDBrowser handles GET /auth/openid/browser — hop 2, opened in the system
// browser by ASWebAuthenticationSession. Cloudflare Access authenticates the
// person in front of this route, so the verified assertion names them.
//
// This GET is SIDE-EFFECT FREE with respect to the flow: it never mints a code
// and never consumes the parked request. With a verified person it renders the
// consent page; minting happens only on that page's POST (OpenIDBrowserConsent).
// Its error answers go to the parked redirect_uri and state, never to anything
// in this request's query.
//
// Identity resolution itself may JIT-provision an allowlisted Cloudflare user,
// exactly as any CF-authenticated ABS request does; the req id is checked first
// so a junk or dead link cannot trigger even that.
func (h *Handler) OpenIDBrowser(c *gin.Context) {
	oidcBrowserHeaders(c)

	reqID, ok := oidcBrowserReqID(c)
	if !ok || !isS256Shaped(reqID) {
		h.oidcBrowserDead(c)
		return
	}
	pending, ok := h.oidcRequests.peek(reqID, h.now())
	if !ok {
		h.oidcBrowserDead(c)
		return
	}

	identity, authErr := h.resolver.ResolveCFAssertion(c)
	noPerson := authErr == nil && (identity == nil || identity.User == nil)
	reauth := c.Query(oidcReauthParam) != ""

	// One line per browser-hop load, first and reauth alike, saying what
	// Cloudflare actually let through: which kind of assertion, and whether a
	// CF_ cookie rode along. This is how to tell, from the journal, which of the
	// two strict_service_token_auth behaviours in the file comment is in effect.
	// Cookie NAMES only, never values.
	assertionKind := oidcAssertionKind(c, identity, authErr)
	cfCookies := oidcCFCookieNames(c)
	oidcLog.Info("openid browser hop load: reauth=%t assertion=%s service_token=%s cf_cookies=%s caller=%s",
		reauth, assertionKind, logger.SanitizeLogValue(servermiddleware.ABSServiceToken(c)),
		cfCookies, logger.SanitizeLogValue(pending.CallerKey))

	// A valid assertion that names no person: on iOS 17.4+ the initial browser
	// load carries the app's service token. Redirect to ourselves once; that load
	// is not the initial URL, so it carries no service-token HEADER, and
	// Cloudflare has to admit it some other way. Whether that is the person
	// depends on the Access configuration — see the file comment.
	if noPerson && strings.TrimSpace(c.GetHeader(oauth.CFAccessHeader)) != "" && !reauth {
		if origin, ok := h.oidcOrigin(c); ok {
			c.Header("Location", oidcBrowserURL(origin, reqID, true))
			c.Status(http.StatusFound)
			return
		}
	}
	if noPerson && reauth && assertionKind == oidcAssertionServiceToken {
		// The bounce did not shed the token: Cloudflare admitted the reload on a
		// service token again — most likely a service-token CF_Authorization
		// cookie in the browser's shared jar (strict_service_token_auth off).
		oidcLog.Warn("openid browser hop reauth load still carried a service-token assertion: service_token=%s cf_cookies=%s caller=%s",
			logger.SanitizeLogValue(servermiddleware.ABSServiceToken(c)), cfCookies,
			logger.SanitizeLogValue(pending.CallerKey))
	}

	ev := h.oidcBrowserAudit(c, pending)
	if authErr != nil {
		oidcLog.Warn("openid browser hop rejected an assertion: reason=%s status=%d email=%s caller=%s",
			authErr.Reason, authErr.Status, logger.SanitizeLogValue(authErr.Email),
			logger.SanitizeLogValue(pending.CallerKey))
		ev.Outcome = oidcOutcomeForStatus(authErr.Status)
		ev.Reason = authErr.Reason
		ev.Email = authErr.Email
		absauth.Audit(ev)
		errCode := "access_denied"
		if authErr.Status >= http.StatusInternalServerError {
			errCode = "server_error"
		}
		oidcRedirectError(c, http.StatusFound, pending.RedirectURI, pending.State, errCode, authErr.Message)
		return
	}
	if noPerson {
		// Nobody was authenticated even after the service-token bounce. In this
		// deployment that means the request reached the origin without Cloudflare
		// Access authenticating a person. Report it through the client's callback:
		// ASWebAuthenticationSession is waiting for exactly that navigation and
		// closes cleanly on it. The parked request is left to expire.
		oidcLog.Warn("openid browser hop resolved no verified identity: service_token=%s reauth=%t caller=%s client_id=%s",
			logger.SanitizeLogValue(ev.ServiceToken), reauth,
			logger.SanitizeLogValue(pending.CallerKey), oidcLogValue(pending.ClientID))
		ev.Outcome = absauth.OutcomeDenied
		ev.Reason = "no-verified-identity"
		absauth.Audit(ev)
		oidcRedirectError(c, http.StatusFound, pending.RedirectURI, pending.State, "access_denied",
			"no verified identity on this request")
		return
	}

	ev.UserID, ev.Username, ev.Email = identity.User.ID, identity.User.Username, identity.Email
	ev.Outcome = absauth.OutcomeSuccess
	ev.Reason = "consent-shown"
	absauth.Audit(ev)
	h.renderOIDCConsent(c, reqID, pending, identity)
}

// OpenIDBrowserConsent handles POST /auth/openid/browser — the consent page's
// Allow or Deny. This is the ONLY place a code is minted for the CF flow.
//
// Three independent checks stand between a cross-site request and a code:
//   - the person is re-resolved from THIS request's verified assertion;
//   - the csrf field must equal oidcConsentToken(req, that person's user id), an
//     HMAC under a per-process secret, so a token is useless for any other
//     request id or any other account, and cannot be computed off-server;
//   - an Origin header, when the browser sends one, must be this origin.
//
// Failures before the take answer inline and do not consume the parked request,
// so a forged POST cannot burn someone's in-progress sign-in.
func (h *Handler) OpenIDBrowserConsent(c *gin.Context) {
	oidcBrowserHeaders(c)

	// Both sides are normalised (oidcNormalizeOrigin), so a Host of
	// "Media.Example.com:443" matches an Origin of "https://media.example.com",
	// while a different scheme, host or port — or "null" — does not.
	if raw := strings.TrimSpace(c.GetHeader("Origin")); raw != "" {
		got, gotOK := oidcNormalizeOrigin(raw, false)
		own, ownOK := h.oidcOrigin(c)
		if !gotOK || !ownOK || got != own {
			h.oidcConsentRefuse(c, "origin-mismatch", "This sign-in request did not come from this server.")
			return
		}
	}
	reqID := c.PostForm("req")
	if !isS256Shaped(reqID) {
		h.oidcBrowserDead(c)
		return
	}
	if _, ok := h.oidcRequests.peek(reqID, h.now()); !ok {
		h.oidcBrowserDead(c)
		return
	}

	identity, authErr := h.resolver.ResolveCFAssertion(c)
	if authErr != nil || identity == nil || identity.User == nil {
		h.oidcConsentRefuse(c, "no-verified-identity",
			"Your sign-in could not be verified. Close this window and sign in again from the app.")
		return
	}
	want := h.oidcConsentToken(reqID, identity.User.ID)
	if subtle.ConstantTimeCompare([]byte(want), []byte(c.PostForm("csrf"))) != 1 {
		h.oidcConsentRefuse(c, "consent-token-invalid",
			"This sign-in confirmation is not valid. Close this window and sign in again from the app.")
		return
	}

	pending, ok := h.oidcRequests.take(reqID, h.now())
	if !ok {
		// Lost a race with a concurrent submit of the same page, or expired.
		h.oidcBrowserDead(c)
		return
	}
	ev := h.oidcBrowserAudit(c, pending)
	ev.UserID, ev.Username, ev.Email = identity.User.ID, identity.User.Username, identity.Email

	// The request is consumed, so its id may now stand in for a state the
	// client did not send (see "The official Audiobookshelf app").
	state := pending.State
	if state == "" {
		state = reqID
	}
	if c.PostForm("decision") != "allow" {
		ev.Outcome = absauth.OutcomeDenied
		ev.Reason = "consent-declined"
		absauth.Audit(ev)
		oidcRedirectError(c, http.StatusSeeOther, pending.RedirectURI, state, "access_denied",
			"the user declined the sign-in")
		return
	}

	code, err := mintOIDCCode(identity.User, identity.Email, pending.Challenge, pending.RedirectURI, h.now())
	if err != nil {
		oidcLog.Error("openid consent could not mint a code: user_id=%s: %v",
			logger.SanitizeLogValue(identity.User.ID), err)
		ev.Outcome = absauth.OutcomeError
		ev.Reason = "code-mint-error"
		absauth.Audit(ev)
		oidcRedirectError(c, http.StatusSeeOther, pending.RedirectURI, state, "server_error", "could not mint code")
		return
	}
	ev.Outcome = absauth.OutcomeSuccess
	ev.Reason = "code-issued"
	absauth.Audit(ev)
	oidcRedirect(c, http.StatusSeeOther, pending.RedirectURI, map[string]string{"code": code, "state": state})
}

// oidcConsentToken is the consent form's CSRF token: HMAC-SHA256 over the req id
// and the user id under a per-process random key. Stateless, bound to one
// request and one account, and invalidated by a restart along with the parked
// requests it protects.
func (h *Handler) oidcConsentToken(reqID, userID string) string {
	m := hmac.New(sha256.New, h.oidcConsentKey)
	m.Write([]byte("abs-openid-consent\x00"))
	m.Write([]byte(reqID))
	m.Write([]byte{0})
	m.Write([]byte(userID))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// oidcBrowserAudit is the common part of every hop-2 audit record.
func (h *Handler) oidcBrowserAudit(c *gin.Context, pending oidcPendingRequest) absauth.AuditEvent {
	st := servermiddleware.ABSServiceToken(c)
	if st == "" && strings.HasPrefix(pending.CallerKey, "svc:") {
		// The hop-1 service token, so the token↔person pairing is on this line.
		st = strings.TrimPrefix(pending.CallerKey, "svc:")
	}
	return absauth.AuditEvent{
		Action: "openid-browser", Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), Path: c.Request.URL.Path,
		UserAgent: c.Request.UserAgent(), ServiceToken: st,
	}
}

// oidcConsentRefuse answers a consent POST that failed a check before the take.
func (h *Handler) oidcConsentRefuse(c *gin.Context, reason, message string) {
	absauth.Audit(absauth.AuditEvent{
		Action: "openid-consent", Outcome: absauth.OutcomeDenied, Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), Reason: reason,
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})
	oidcBrowserMessage(c, http.StatusForbidden, "Sign-in not confirmed", message)
}

// oidcBrowserDead answers hop 2 for an unknown, expired or used id. There is no
// trusted redirect target for such an id, so this renders a message page in the browser
// rather than redirecting anywhere.
func (h *Handler) oidcBrowserDead(c *gin.Context) {
	absauth.Audit(absauth.AuditEvent{
		Action: "openid-browser", Outcome: absauth.OutcomeFailure, Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), Reason: "request-unknown-expired-or-used",
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})
	oidcBrowserMessage(c, http.StatusBadRequest, "Sign-in link expired",
		"This sign-in link is invalid, has expired, or was already used. Close this window and sign in again from the app.")
}

// oidcBrowserMessage renders oidcMessageTemplate and aborts. html/template
// escapes both values; neither is caller-supplied today.
func oidcBrowserMessage(c *gin.Context, status int, title, message string) {
	var buf bytes.Buffer
	if err := oidcMessageTemplate.Execute(&buf, map[string]string{"Title": title, "Message": message}); err != nil {
		oidcLog.Error("openid message page failed to render: %v", err)
		c.String(status, message)
		c.Abort()
		return
	}
	c.Data(status, "text/html; charset=utf-8", buf.Bytes())
	c.Abort()
}

// oidcKnownClients names apps by their registered callback scheme. The consent
// page names the app from the ALLOW-LISTED redirect_uri, never from the
// caller-supplied client_id, which anyone starting hop 1 can set to anything.
var oidcKnownClients = map[string]string{"audiobooth": "AudioBooth", "audiobookshelf": "Audiobookshelf"}

// oidcPageStyle is shared by every hop-2 page. Inline because the pages' CSP
// (oidcBrowserHeaders) allows inline style and nothing else.
const oidcPageStyle = `<style>
:root{color-scheme:light dark;--bg:#f6f6f4;--fg:#1c1c1a;--muted:#5d5d58;--card:#fff;--accent:#2f5d50;--line:#d9d9d4}
@media (prefers-color-scheme:dark){:root{--bg:#161615;--fg:#ececea;--muted:#a3a39d;--card:#20201f;--accent:#7fb8a6;--line:#3a3a37}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 -apple-system,system-ui,sans-serif}
main{max-width:26rem;margin:12vh auto 0;padding:1.5rem 1rem}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:1.5rem}
h1{font-size:1.25rem;margin:0 0 1rem}
.who{font-weight:600;word-break:break-all}
.small{color:var(--muted);font-size:.9rem}
code{font-size:.9rem}
.row{display:flex;gap:.75rem;margin-top:1.25rem}
button{flex:1;font:inherit;padding:.7rem 1rem;border-radius:8px;border:1px solid var(--line);background:transparent;color:var(--fg)}
button.allow{background:var(--accent);border-color:var(--accent);color:var(--card);font-weight:600}
</style>`

// oidcMessageTemplate is the page for a hop-2 failure that has no trusted
// redirect target (a dead link, a refused consent POST). A page rather than
// text/plain so it is readable at phone width inside the sign-in sheet.
var oidcMessageTemplate = template.Must(template.New("message").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
` + oidcPageStyle + `</head>
<body><main><div class="card">
<h1>{{.Title}}</h1>
<p>{{.Message}}</p>
</div></main></body></html>
`))

var oidcConsentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in to {{.App}}?</title>
` + oidcPageStyle + `</head>
<body><main><div class="card">
<h1>Sign in to {{.App}}?</h1>
<p>{{.App}} is asking to sign in to this server as</p>
<p class="who">{{.Account}}</p>
<p class="small">The app gets access to your library as this account. You will be returned to <code>{{.Scheme}}://</code>.</p>
<p class="small">If you did not just start signing in from {{.App}}, choose Deny.</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="req" value="{{.Req}}">
<input type="hidden" name="csrf" value="{{.Token}}">
<div class="row">
<button type="submit" name="decision" value="deny">Deny</button>
<button type="submit" name="decision" value="allow" class="allow">Allow</button>
</div>
</form>
</div></main></body></html>
`))

// renderOIDCConsent writes the consent page. html/template escapes every value;
// the only caller-influenced text on it is the redirect_uri's scheme, which is
// already on the allowlist.
func (h *Handler) renderOIDCConsent(c *gin.Context, reqID string, pending oidcPendingRequest, identity *servermiddleware.ABSIdentity) {
	scheme := pending.RedirectURI
	if i := strings.Index(scheme, ":"); i >= 0 {
		scheme = scheme[:i]
	}
	app := oidcKnownClients[strings.ToLower(scheme)]
	if app == "" {
		app = "The app"
	}
	account := strings.TrimSpace(identity.Email)
	if account == "" {
		account = identity.User.Username
	}
	var buf bytes.Buffer
	if err := oidcConsentTemplate.Execute(&buf, map[string]string{
		"App": app, "Account": account, "Scheme": scheme,
		"Action": OIDCBrowserPath, "Req": reqID, "Token": h.oidcConsentToken(reqID, identity.User.ID),
	}); err != nil {
		oidcLog.Error("openid consent page failed to render: %v", err)
		c.String(http.StatusInternalServerError, "Could not render the sign-in page.")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
}

// oidcBrowserHeaders hardens every hop-2 response: no caching (they carry
// request ids and codes), no cross-origin referrer, and no framing — a consent
// page that can be framed can be clickjacked into an Allow. No form-action
// directive: Chrome applies it to the redirect after a submit, which would block
// the callback scheme.
//
// Referrer-Policy is same-origin, NOT no-referrer. Under no-referrer the Fetch
// spec sends `Origin: null` on the consent form's POST, which the Origin check
// in OpenIDBrowserConsent refuses — every real Allow would 403. Measured
// 2026-10-19 with Playwright: WebKit (what ASWebAuthenticationSession runs) and
// Chromium both send `Origin: null` under no-referrer and the page's real
// origin under same-origin. same-origin still sends no referrer off-origin; the
// only off-origin navigation from hop 2 is the custom-scheme callback.
func oidcBrowserHeaders(c *gin.Context) {
	oidcNoStore(c)
	c.Header("Referrer-Policy", "same-origin")
	c.Header("X-Frame-Options", "DENY")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
}

// OpenIDCallback handles GET /auth/openid/callback — the code-for-session
// exchange, called by the app's own HTTP client.
//
// This returns JSON, not a redirect: AudioBooth decodes the body as Authorize.
// It deliberately does NOT require a Cloudflare identity assertion, because this
// hop is authenticated to the edge by a service token (a machine credential with
// no email). The code plus its PKCE verifier is the identity.
func (h *Handler) OpenIDCallback(c *gin.Context) {
	oidcNoStore(c)
	code := strings.TrimSpace(c.Query("code"))
	verifier := strings.TrimSpace(c.Query("code_verifier"))
	if code == "" || verifier == "" {
		respondError(c, http.StatusBadRequest, "code and code_verifier are required")
		return
	}

	now := h.now()
	pending, ok := oidcCodes.take(code, now) // single-use: removed even if it then fails
	if !ok {
		respondError(c, http.StatusUnauthorized, "invalid, expired or already-used code")
		return
	}

	// PKCE: the challenge was BASE64URL(SHA256(verifier)) without padding.
	// Compared in constant time — a byte-wise early exit would leak how much of a
	// guessed verifier was correct.
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(pending.Challenge)) != 1 {
		absauth.Audit(absauth.AuditEvent{
			Action: "openid-callback", Outcome: absauth.OutcomeDenied, Mode: oidcAuthMethod,
			SourceIP: strings.TrimSpace(c.ClientIP()), UserID: pending.UserID,
			Reason: "pkce-mismatch", Path: c.Request.URL.Path,
		})
		respondError(c, http.StatusUnauthorized, "code_verifier does not match code_challenge")
		return
	}

	// redirect_uri binding. AudioBooth's loginWithOIDC sends only code,
	// code_verifier and state, so a missing redirect_uri is accepted: the code was
	// only ever delivered to its bound, allow-listed target, and PKCE (above) is
	// what binds it to the client that started the flow (OAuth 2.1 drops
	// redirect_uri from the token request for exactly that reason). A client that
	// DOES send one must send the exact value the code was minted for.
	if ru, sent := c.GetQuery("redirect_uri"); sent && ru != pending.RedirectURI {
		absauth.Audit(absauth.AuditEvent{
			Action: "openid-callback", Outcome: absauth.OutcomeDenied, Mode: oidcAuthMethod,
			SourceIP: strings.TrimSpace(c.ClientIP()), UserID: pending.UserID,
			Reason: "redirect-uri-mismatch", Path: c.Request.URL.Path,
		})
		respondError(c, http.StatusUnauthorized, "redirect_uri does not match the authorization request")
		return
	}

	user, err := h.store.GetUserByID(pending.UserID)
	if err != nil {
		// Transient store failure is a 500, never a 401: telling the client its
		// credential is dead when it is not would wedge it (§1.7.3 item 3).
		respondError(c, http.StatusInternalServerError, "could not load user")
		return
	}
	if user == nil {
		respondError(c, http.StatusUnauthorized, "user no longer exists")
		return
	}
	// An account disabled since the code was minted must not be revived by
	// redeeming it. Mirrors the middleware's own active check.
	if status := strings.ToLower(strings.TrimSpace(user.Status)); status != "" && status != "active" {
		absauth.Audit(absauth.AuditEvent{
			Action: "openid-callback", Outcome: absauth.OutcomeDenied, Mode: oidcAuthMethod,
			SourceIP: strings.TrimSpace(c.ClientIP()), UserID: user.ID,
			Username: user.Username, Reason: "user-inactive", Path: c.Request.URL.Path,
		})
		respondError(c, http.StatusForbidden, "account is not active")
		return
	}
	// The account's credentials or status changed after the code was minted
	// (password change, reset, deactivation): the sign-in that produced it
	// predates the change, so it must not yield a session stamped with the new
	// epoch. The user signs in again.
	if user.CredentialEpoch != pending.CredentialEpoch {
		absauth.Audit(absauth.AuditEvent{
			Action: "openid-callback", Outcome: absauth.OutcomeDenied, Mode: oidcAuthMethod,
			SourceIP: strings.TrimSpace(c.ClientIP()), UserID: user.ID,
			Username: user.Username, Reason: "credentials-changed", Path: c.Request.URL.Path,
		})
		respondError(c, http.StatusUnauthorized, "the account changed since this sign-in started; sign in again")
		return
	}

	// Same session-issuing path as POST /login, so the response shape, the audit
	// record and the token lifetimes cannot drift between password and SSO login.
	h.issueSession(c, user, nil, oidcAuthMethod, pending.Email, now)
}

// oidcRateKey is hop 1's rate-limit key: the verified credential plus gin's
// c.ClientIP().
//
// c.ClientIP() honours the router's trusted-proxy list (server.go
// SetTrustedProxies). Today that list is empty, so behind the shared reverse
// proxies the address is the proxy's and the key is effectively per credential;
// if a trusted-proxy allowlist is ever configured, this becomes per device with
// no change here. Cf-Connecting-Ip and X-Forwarded-For are deliberately NOT read
// directly: the origin cannot tell whether a request really came through
// Cloudflare, so a credential holder reaching the origin directly could set
// them to anything and mint fresh rate-limit buckets at will.
func oidcRateKey(c *gin.Context, callerKey string) string {
	return "openid:" + callerKey + "|" + strings.TrimSpace(c.ClientIP())
}

// oidcLogValue sanitizes a caller-supplied value for a log line and caps it at
// oidcMaxLogValueLen bytes, so a hop-1 caller cannot write arbitrarily long
// lines (a redirect_uri is bounded only by the server's header limit).
func oidcLogValue(s string) string {
	if len(s) > oidcMaxLogValueLen {
		s = s[:oidcMaxLogValueLen] + "…(truncated)"
	}
	return logger.SanitizeLogValue(s)
}

// oidcNoStore keeps every hop out of caches: their Locations carry a request id
// or a code.
func oidcNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
}

// oidcOrigin returns the origin hop 2 lives on, as https://host[:port] with the
// host lowercased and a default :443 dropped (oidcNormalizeOrigin).
//
// Precedence:
//
//  1. The configured public URL (Options.PublicOrigin: oauth_redirect_base_url,
//     else the server's --external-url), reduced to its origin. Preferred because
//     it does not depend on every proxy in front of the origin preserving Host.
//  2. Otherwise the Host this request arrived with. Production sets neither
//     setting today, so this is what runs there.
//
// The caveat that decides which to set: hop 2 must land on the same hostname —
// and so the same Cloudflare Access application and AUD — that admitted hop 1.
// A public URL configured for the web UI's hostname would move hop 2 to a
// hostname whose Access application may have a different AUD, and every SSO
// sign-in would then fail its assertion check. Hop 1 logs whenever the two
// disagree (see OpenIDAuthorize).
//
// The scheme is always https: ASWebAuthenticationSession needs an absolute
// http(s) URL, and SSO only ever completes behind Cloudflare, which terminates
// TLS. A configured URL that is not https falls back to Host with a warning; a
// Host that does not parse as a bare host[:port] is refused rather than echoed
// into a Location.
func (h *Handler) oidcOrigin(c *gin.Context) (string, bool) {
	if h.oidcPublicOrigin != nil {
		if raw := strings.TrimSpace(h.oidcPublicOrigin()); raw != "" {
			if o, ok := oidcNormalizeOrigin(raw, true); ok {
				return o, true
			}
			oidcLog.Warn("openid: the configured public URL %s is not an https URL with a host; using the request Host instead",
				oidcLogValue(raw))
		}
	}
	return oidcHostOrigin(c.Request.Host)
}

// oidcHostOrigin is the origin for a bare Host header value.
func oidcHostOrigin(host string) (string, bool) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, "/\\@?#% \t\r\n") {
		return "", false
	}
	return oidcNormalizeOrigin("https://"+host, false)
}

// oidcNormalizeOrigin reduces raw to "https://host[:port]": scheme https only,
// host lowercased, a default :443 dropped, IPv6 bracketed. Every origin this
// file compares — the Origin header, the request Host, a configured URL — goes
// through here, so equal origins compare equal as strings, scheme included.
//
// raw must be an https URL with a host and no userinfo, query or fragment.
// allowPath admits (and discards) a path, for a configured base URL such as
// "https://books.example.com/"; an Origin header or a Host never has one.
// "null" — the Origin a sandboxed or no-referrer document sends — fails.
func oidcNormalizeOrigin(raw string, allowPath bool) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (!allowPath && u.Path != "") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && port != "443" {
		host += ":" + port
	}
	return "https://" + host, true
}

// oidcDefaultClientID stands in for a client_id the app did not send: the
// official app refuses a hop-1 Location without one. It names nothing and is
// never displayed (the consent page names the app from redirect_uri).
const oidcDefaultClientID = "abs-client"

// oidcHop1Location is hop 1's Location: hop 2 on our origin, carrying the
// request id as both `req` (AudioBooth opens this URL verbatim) and `state`
// (the official app rebuilds the URL keeping only client_id, scope, state and
// redirect_uri), plus those echoes for the official app's presence check.
// scope is always "openid": hop 2 ignores it, so echoing the caller's would
// only reflect input. client_id is at most oidcMaxClientIDLen by now.
func oidcHop1Location(origin, reqID, clientID, redirectURI string) string {
	if clientID == "" {
		clientID = oidcDefaultClientID
	}
	q := url.Values{}
	q.Set("req", reqID)
	q.Set("state", reqID)
	q.Set("client_id", clientID)
	q.Set("scope", "openid")
	q.Set("redirect_uri", redirectURI)
	return origin + OIDCBrowserPath + "?" + q.Encode()
}

// oidcBrowserReqID returns the parked request's id from a hop-2 GET: `req`, or
// `state` when the official app's rebuild dropped `req`. Each may appear at
// most once, and when both are present they must agree; anything else is
// ambiguous and refused rather than guessed at.
func oidcBrowserReqID(c *gin.Context) (string, bool) {
	reqs, states := c.QueryArray("req"), c.QueryArray("state")
	if len(reqs) > 1 || len(states) > 1 {
		return "", false
	}
	switch {
	case len(reqs) == 1 && (len(states) == 0 || states[0] == reqs[0]):
		return reqs[0], true
	case len(reqs) == 0 && len(states) == 1:
		return states[0], true
	default:
		return "", false
	}
}

// oidcBrowserURL is hop 2's absolute URL for a parked request, as used by
// hop 2's own reauth bounce (the browser follows it directly, so it needs no
// echoes for the official app).
func oidcBrowserURL(origin, reqID string, reauth bool) string {
	u := origin + OIDCBrowserPath + "?req=" + url.QueryEscape(reqID)
	if reauth {
		u += "&" + oidcReauthParam + "=1"
	}
	return u
}

// isS256Shaped reports whether s is exactly 43 unpadded base64url characters —
// the shape of every S256 challenge and of every id NewRefreshSeed returns.
func isS256Shaped(s string) bool {
	if len(s) != oidcS256ChallengeLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_':
		default:
			return false
		}
	}
	return true
}

// Assertion kinds for the hop-2 load log line.
const (
	oidcAssertionNone         = "none"          // no Cf-Access-Jwt-Assertion header
	oidcAssertionRejected     = "rejected"      // present but refused by the resolver
	oidcAssertionServiceToken = "service-token" // verified, a service token
	oidcAssertionNoPerson     = "no-person"     // verified, names no person, not a token
	oidcAssertionPerson       = "person"        // verified, names a provisioned person
)

// oidcAssertionKind classifies what Cloudflare forwarded on a hop-2 load.
func oidcAssertionKind(c *gin.Context, identity *servermiddleware.ABSIdentity, authErr *servermiddleware.ABSAuthError) string {
	switch {
	case strings.TrimSpace(c.GetHeader(oauth.CFAccessHeader)) == "":
		return oidcAssertionNone
	case authErr != nil:
		return oidcAssertionRejected
	case identity != nil && identity.User != nil:
		return oidcAssertionPerson
	case servermiddleware.ABSServiceToken(c) != "":
		return oidcAssertionServiceToken
	default:
		return oidcAssertionNoPerson
	}
}

// oidcCFCookieNames lists the names of the CF_-prefixed cookies on the request
// ("-" for none). Names only — the same convention as ABSAuthProbe, which also
// matches by prefix because Access can be configured with a custom cookie name.
func oidcCFCookieNames(c *gin.Context) string {
	var names []string
	for _, ck := range c.Request.Cookies() {
		if strings.HasPrefix(strings.ToUpper(ck.Name), "CF_") {
			names = append(names, ck.Name)
		}
	}
	if len(names) == 0 {
		return "-"
	}
	return oidcLogValue(strings.Join(names, ","))
}

// oidcOutcomeForStatus classifies a resolver error for the audit log.
func oidcOutcomeForStatus(status int) absauth.Outcome {
	switch {
	case status >= http.StatusInternalServerError:
		return absauth.OutcomeError
	case status == http.StatusForbidden:
		return absauth.OutcomeDenied
	default:
		return absauth.OutcomeFailure
	}
}

// oidcRedirect redirects to target with params appended, skipping empty values.
// status is 302 from hop 2's GET and 303 from the consent POST.
//
// The Location header is written directly rather than via c.Redirect because the
// target is a custom scheme (audiobooth://oauth) — this keeps the value exactly
// as the client registered it. Only hop 2 calls this; hop 1 never redirects to
// the client's target.
func oidcRedirect(c *gin.Context, status int, target string, params map[string]string) {
	c.Header("Location", buildOIDCRedirect(target, params))
	c.Status(status)
}

// buildOIDCRedirect assembles the callback URL. Separated from the gin plumbing
// so the exact string can be asserted in tests — the custom scheme surviving
// unmangled is the difference between the app waking up and the login silently
// dying, and that is worth pinning rather than eyeballing.
//
// Empty values are omitted rather than emitted as `state=`, which some clients
// read as present-but-mismatched.
func buildOIDCRedirect(target string, params map[string]string) string {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	var b strings.Builder
	b.WriteString(target)
	first := true
	for _, k := range []string{"code", "error", "error_description", "state"} {
		v, present := params[k]
		if !present || v == "" {
			continue
		}
		if first {
			b.WriteString(sep)
			first = false
		} else {
			b.WriteString("&")
		}
		fmt.Fprintf(&b, "%s=%s", k, url.QueryEscape(v))
	}
	return b.String()
}

// oidcRedirectError reports a hop-2 failure through the client's own callback so
// its web session closes with a message instead of hanging.
func oidcRedirectError(c *gin.Context, status int, target, state, code, description string) {
	oidcRedirect(c, status, target, map[string]string{
		"error":             code,
		"error_description": description,
		"state":             state,
	})
}
