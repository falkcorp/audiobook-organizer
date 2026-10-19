// file: internal/server/handlers/abs/openid.go
// version: 2.2.0
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
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

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
// # The flow now: two hops, like real ABS
//
//	hop 1  GET  /auth/openid           (app's URLSession, service token only)
//	       require a verified Cloudflare credential, rate-limit and quota per
//	       (credential, device) bucket, validate redirect_uri + PKCE, park the
//	       request under a random id,
//	       302 → https://<this host>/auth/openid/browser?req=<id>
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
// On iOS 17.4+ the browser's first load of hop 2 carries the service token too.
// If Cloudflare admits that load on the token, the origin sees a valid but
// person-less assertion. Hop 2 then redirects ONCE to itself with reauth=1:
// that second load is not the initial URL, so it carries no service token, and
// Cloudflare must authenticate the person (by their Access cookie or a fresh IdP
// login). Only after that does a missing identity become a terminal
// access_denied.
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

	// oidcStoreCap is the most entries either in-memory store holds. When the
	// store is full the OLDEST entry is evicted, so a flood can never pin the
	// store shut: new sign-ins always get in. Human login rates are a handful an
	// hour; 1024 live entries is far beyond any legitimate peak.
	oidcStoreCap = 1024

	// oidcPerBucketCap is the most live parked requests (or unredeemed codes)
	// one bucket may hold. A new one evicts that bucket's own oldest, so a
	// person who abandons a sign-in and retries is never blocked, and a caller
	// flooding hop 1 only ever churns its own slots.
	//
	// A hop-1 bucket is (verified credential, device address), NOT the
	// credential alone: a Cloudflare service token is shared by a whole group of
	// people and devices (see oidcBucketKey), so a 3-slot cap on the token would
	// let a fourth concurrent sign-in anywhere in the group evict a real one.
	oidcPerBucketCap = 3

	// Hop-1 rate limits. Per bucket: generous for a person retrying, tight for
	// one device. Per credential: a CEILING over all of a credential's buckets,
	// because the device address is unauthenticated and a credential holder can
	// rotate it to mint fresh buckets. The ceiling bounds one credential to
	// about oidcCredRatePerMinute*5min = 300 live parked requests, under
	// oidcStoreCap, so no single credential can churn the global store and
	// evict other groups' sign-ins.
	oidcRatePerMinute     = 10
	oidcRateBurst         = 5
	oidcCredRatePerMinute = 60
	oidcCredRateBurst     = 30

	// oidcAuthMethod labels this path in audit records and ABSIdentity.Mode.
	oidcAuthMethod = "openid"

	// OIDCBrowserPath is hop 2, opened in the system browser.
	OIDCBrowserPath = "/auth/openid/browser"

	// oidcReauthParam marks hop 2's single self-redirect (see the file comment).
	oidcReauthParam = "reauth"

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
// Bounds, both enforced on put and both by EVICTION, never refusal:
//   - per owner: at most perOwner live entries; a new one evicts that owner's
//     oldest;
//   - global: at most max entries; expired ones are swept first, then the
//     oldest live one is evicted.
//
// The scans are O(n) with n ≤ max (1024), on a path that runs a handful of times
// an hour and is rate-limited per caller, so a heap or per-owner index would be
// complexity without a payoff.
type oidcTTLStore[V any] struct {
	mu       sync.Mutex
	entries  map[string]*oidcTTLEntry[V]
	max      int
	perOwner int
	seq      uint64
	purgeAt  time.Time
}

type oidcTTLEntry[V any] struct {
	value     V
	owner     string
	expiresAt time.Time
	seq       uint64 // insertion order; lower is older
}

// oidcPutResult reports what a put had to evict to make room.
type oidcPutResult struct {
	OwnerEvicted  int // the owner's own oldest entries, over perOwner
	GlobalEvicted int // someone's oldest live entry, at the global cap
}

func newOIDCTTLStore[V any](maxEntries, perOwner int) *oidcTTLStore[V] {
	return &oidcTTLStore[V]{entries: map[string]*oidcTTLEntry[V]{}, max: maxEntries, perOwner: perOwner}
}

// put registers key, owned by owner, until now+ttl. It always succeeds; see the
// type comment for what it evicts. An empty owner is exempt from the per-owner
// bound.
func (s *oidcTTLStore[V]) put(key, owner string, value V, now time.Time, ttl time.Duration) oidcPutResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res oidcPutResult
	delete(s.entries, key) // re-putting a key replaces it and frees its slot
	if len(s.entries) >= s.max || now.Sub(s.purgeAt) > time.Minute {
		for k, e := range s.entries {
			if !now.Before(e.expiresAt) {
				delete(s.entries, k)
			}
		}
		s.purgeAt = now
	}
	if owner != "" && s.perOwner > 0 {
		for s.countOwnerLocked(owner) >= s.perOwner {
			s.evictOldestLocked(owner)
			res.OwnerEvicted++
		}
	}
	for len(s.entries) >= s.max {
		s.evictOldestLocked("")
		res.GlobalEvicted++
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

// evictOldestLocked removes the oldest entry, or the oldest of owner's when
// owner is non-empty.
func (s *oidcTTLStore[V]) evictOldestLocked(owner string) {
	oldestKey, oldestSeq, found := "", uint64(0), false
	for k, e := range s.entries {
		if owner != "" && e.owner != owner {
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
}

// oidcPendingRequest is one hop-1 request parked until the browser arrives.
// redirect_uri and state are taken from HERE at hop 2, never from hop 2's own
// query string, so the browser hop cannot be steered somewhere else.
type oidcPendingRequest struct {
	Challenge   string
	RedirectURI string
	State       string
	ClientID    string
	// Hop 1's verified Cloudflare credential key (svc:<token> or email:<…>),
	// for the hop-2 audit line. The quota bucket is in the store entry's owner.
	CallerKey string
	// Hop 1's client, recorded for the audit line. Not enforced: Safari with
	// iCloud Private Relay legitimately reaches us from a different address than
	// the app's own URLSession, and behind the shared proxies every caller has
	// the same address anyway.
	SourceIP  string
	UserAgent string
}

// oidcCodes is package-level because the web OAuth deep-link flow
// (internal/server/handlers) mints into it through MintAppAuthorizationCode.
// Codes are owned by the user they were minted for.
var oidcCodes = newOIDCTTLStore[oidcPendingCode](oidcStoreCap, oidcPerBucketCap)

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
// challenge is required. A code minted without one would be a bearer credential
// sitting in a URL on a custom scheme that any other installed app can register —
// which is precisely the takeover PKCE exists to stop — so this refuses rather than
// defaulting to an unbound code. redirectURI must be on the allowlist, re-checked
// here so no caller can bind a code to a target the allowlist never approved.
func MintAppAuthorizationCode(userID, email, challenge, redirectURI string) (string, error) {
	return mintOIDCCode(userID, email, challenge, redirectURI, time.Now())
}

func mintOIDCCode(userID, email, challenge, redirectURI string, now time.Time) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", errors.New("abs: cannot mint an authorization code with no user")
	}
	if strings.TrimSpace(challenge) == "" {
		return "", errors.New("abs: cannot mint an authorization code with no PKCE challenge")
	}
	if !oidcRedirectAllowed(redirectURI) {
		return "", errors.New("abs: cannot mint an authorization code for an unregistered redirect_uri")
	}
	code, err := absauth.NewRefreshSeed() // 256-bit URL-safe random; not a refresh token
	if err != nil {
		return "", err
	}
	oidcCodes.put(code, userID, oidcPendingCode{
		UserID:      userID,
		Email:       email,
		Challenge:   challenge,
		RedirectURI: redirectURI,
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
// app's service token, or a person's assertion), rate-limits per credential and
// per (credential, device) bucket, quotas per bucket, validates redirect_uri and
// PKCE, parks the request under a random single-use id, and 302s to hop 2 on
// this server's own https origin.
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
			logger.SanitizeLogValue(redirectURI), logger.SanitizeLogValue(strings.TrimSpace(c.ClientIP())))
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
	bucket := oidcBucketKey(c, callerKey)
	if !h.oidcCredLimiter.AllowKey("openid-cred:" + callerKey) {
		oidcLog.Warn("openid authorize rate-limited at the credential ceiling: caller=%s", logger.SanitizeLogValue(callerKey))
		h.oidcAuthorizeReject(c, "rate-limited-credential", "Too many sign-in attempts. Wait a minute and try again.")
		return
	}
	if !h.oidcLimiter.AllowKey("openid:" + bucket) {
		oidcLog.Warn("openid authorize rate-limited: bucket=%s", logger.SanitizeLogValue(bucket))
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
	origin, ok := oidcRequestOrigin(c)
	if !ok {
		h.oidcAuthorizeReject(c, "host-invalid", "invalid Host header")
		return
	}

	id, err := absauth.NewRefreshSeed() // 256-bit URL-safe random
	if err != nil {
		oidcLog.Error("openid authorize could not generate a request id: %v", err)
		h.oidcAuthorizeReject(c, "request-id-error", "Could not start sign-in. Try again.")
		return
	}
	res := h.oidcRequests.put(id, bucket, oidcPendingRequest{
		Challenge:   challenge,
		RedirectURI: redirectURI,
		State:       state,
		ClientID:    clientID,
		CallerKey:   callerKey,
		SourceIP:    strings.TrimSpace(c.ClientIP()),
		UserAgent:   c.Request.UserAgent(),
	}, h.now(), oidcRequestTTL)
	if res.GlobalEvicted > 0 {
		// Only reachable when many distinct credentials are parking requests at
		// once: a flood signal worth a line.
		oidcLog.Warn("openid authorize evicted %d parked request(s) at the global cap %d (bucket=%s)",
			res.GlobalEvicted, oidcStoreCap, logger.SanitizeLogValue(bucket))
	}

	absauth.Audit(absauth.AuditEvent{
		Action: "openid-authorize", Outcome: absauth.OutcomeSuccess, Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), Reason: "browser-hop-issued",
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		ServiceToken: servermiddleware.ABSServiceToken(c),
	})
	c.Header("Location", oidcBrowserURL(origin, id, false))
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

	reqID := c.Query("req")
	if !isS256Shaped(reqID) {
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

	// A valid assertion that names no person: on iOS 17.4+ the initial browser
	// load carries the app's service token. Redirect to ourselves once; that load
	// is not the initial URL, carries no service token, and so makes Cloudflare
	// authenticate the person.
	if noPerson && strings.TrimSpace(c.GetHeader(oauth.CFAccessHeader)) != "" && c.Query(oidcReauthParam) == "" {
		if origin, ok := oidcRequestOrigin(c); ok {
			oidcLog.Info("openid browser hop carried a non-person assertion (service_token=%s); redirecting once to shed it",
				logger.SanitizeLogValue(servermiddleware.ABSServiceToken(c)))
			c.Header("Location", oidcBrowserURL(origin, reqID, true))
			c.Status(http.StatusFound)
			return
		}
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
			logger.SanitizeLogValue(ev.ServiceToken), c.Query(oidcReauthParam) != "",
			logger.SanitizeLogValue(pending.CallerKey), logger.SanitizeLogValue(pending.ClientID))
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

	if origin := strings.TrimSpace(c.GetHeader("Origin")); origin != "" {
		if own, ok := oidcRequestOrigin(c); !ok || origin != own {
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

	if c.PostForm("decision") != "allow" {
		ev.Outcome = absauth.OutcomeDenied
		ev.Reason = "consent-declined"
		absauth.Audit(ev)
		oidcRedirectError(c, http.StatusSeeOther, pending.RedirectURI, pending.State, "access_denied",
			"the user declined the sign-in")
		return
	}

	code, err := mintOIDCCode(identity.User.ID, identity.Email, pending.Challenge, pending.RedirectURI, h.now())
	if err != nil {
		oidcLog.Error("openid consent could not mint a code: user_id=%s: %v",
			logger.SanitizeLogValue(identity.User.ID), err)
		ev.Outcome = absauth.OutcomeError
		ev.Reason = "code-mint-error"
		absauth.Audit(ev)
		oidcRedirectError(c, http.StatusSeeOther, pending.RedirectURI, pending.State, "server_error", "could not mint code")
		return
	}
	ev.Outcome = absauth.OutcomeSuccess
	ev.Reason = "code-issued"
	absauth.Audit(ev)
	oidcRedirect(c, http.StatusSeeOther, pending.RedirectURI, map[string]string{"code": code, "state": pending.State})
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
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusForbidden, message)
	c.Abort()
}

// oidcBrowserDead answers hop 2 for an unknown, expired or used id. There is no
// trusted redirect target for such an id, so this renders text in the browser
// rather than redirecting anywhere.
func (h *Handler) oidcBrowserDead(c *gin.Context) {
	absauth.Audit(absauth.AuditEvent{
		Action: "openid-browser", Outcome: absauth.OutcomeFailure, Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), Reason: "request-unknown-expired-or-used",
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusBadRequest,
		"This sign-in link is invalid, has expired, or was already used. Close this window and sign in again from the app.")
	c.Abort()
}

// oidcKnownClients names apps by their registered callback scheme. The consent
// page names the app from the ALLOW-LISTED redirect_uri, never from the
// caller-supplied client_id, which anyone starting hop 1 can set to anything.
var oidcKnownClients = map[string]string{"audiobooth": "AudioBooth"}

var oidcConsentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in to {{.App}}?</title>
<style>
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
</style></head>
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
// request ids and codes), no referrer, and no framing — a consent page that can
// be framed can be clickjacked into an Allow. No form-action directive: Chrome
// applies it to the redirect after a submit, which would block the callback
// scheme.
func oidcBrowserHeaders(c *gin.Context) {
	oidcNoStore(c)
	c.Header("Referrer-Policy", "no-referrer")
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

	// Same session-issuing path as POST /login, so the response shape, the audit
	// record and the token lifetimes cannot drift between password and SSO login.
	h.issueSession(c, user, nil, oidcAuthMethod, pending.Email, now)
}

// oidcBucketKey is hop 1's quota and rate-limit bucket: the verified credential
// plus the device's address.
//
// The credential is necessary but not sufficient. A person's assertion names one
// person, but a service token is shared by a group (the owner mints one per
// friends/family/… group), so every device in the group presents the same key.
// The device address separates them. It comes from Cf-Connecting-Ip, which
// Cloudflare sets to the client's address: the origin trusts no forwarded
// headers (server.go SetTrustedProxies(nil)), and behind the shared reverse
// proxies c.ClientIP() is the proxy's address, identical for everyone.
//
// Cf-Connecting-Ip is NOT authenticated at the origin; a credential holder who
// reaches the origin directly can set it to anything. That is why it is used
// ONLY to split a credential's quota into buckets, never to decide access, and
// why a per-credential rate ceiling sits above the buckets: rotating the header
// buys an attacker fresh buckets of their OWN credential's ceiling, and nothing
// from anyone else's. AudioBooth sends no per-device identifier on hop 1
// (OIDCAuthenticationManager sends only the URL and its custom headers), so
// there is nothing better to use.
func oidcBucketKey(c *gin.Context, callerKey string) string {
	addr := strings.TrimSpace(c.GetHeader("Cf-Connecting-Ip"))
	if ip := net.ParseIP(addr); ip != nil {
		addr = ip.String()
	} else {
		addr = strings.TrimSpace(c.ClientIP())
	}
	return callerKey + "|ip:" + addr
}

// oidcNoStore keeps every hop out of caches: their Locations carry a request id
// or a code.
func oidcNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
}

// oidcRequestOrigin returns https://<Host> for this request.
//
// The origin is the Host the app itself reached, not a configured base URL: hop
// 2 must land on the same hostname — and so the same Cloudflare Access
// application and AUD — that admitted hop 1. The scheme is always https because
// ASWebAuthenticationSession needs an absolute http(s) URL and SSO only ever
// completes behind Cloudflare, which terminates TLS. A Host that does not parse
// as a bare host[:port] is refused rather than echoed into a Location.
func oidcRequestOrigin(c *gin.Context) (string, bool) {
	host := strings.TrimSpace(c.Request.Host)
	if host == "" || strings.ContainsAny(host, "/\\@?#% \t\r\n") {
		return "", false
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host || u.User != nil || u.Path != "" || u.Hostname() == "" {
		return "", false
	}
	return "https://" + host, true
}

// oidcBrowserURL is hop 2's absolute URL for a parked request.
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
