// file: internal/server/handlers/abs/openid.go
// version: 2.0.0
// guid: 3b8e5a14-70c9-4f26-9d51-a2c60f7b8e93
// last-edited: 2026-10-19

package abs

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
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
//	hop 1  GET /auth/openid            (app's URLSession, service token only)
//	       validate redirect_uri + PKCE, park the request under a random id,
//	       302 → https://<this host>/auth/openid/browser?req=<id>
//	       errors: 400 text/plain, never a redirect
//
//	hop 2  GET /auth/openid/browser    (system browser — Cloudflare Access
//	                                    authenticates the PERSON here)
//	       resolve the person from the verified assertion, consume the parked
//	       request, mint a code bound to its challenge and redirect_uri,
//	       302 → <redirect_uri>?code=…&state=…
//	       errors: 302 → <redirect_uri>?error=access_denied&state=…
//	       (safe here: ASWebAuthenticationSession is waiting for exactly that)
//
//	hop 3  GET /auth/openid/callback   (app's URLSession, service token only)
//	       redeem code + code_verifier → Authorize JSON via issueSession
//
// Hop 1 resolves no identity at all, even when a person assertion is present,
// and never redirects to a custom scheme. It is the hop the app inspects with
// its own HTTP client, so the only thing it may ever hand back is an https URL.
//
// # The browser hop and the service token
//
// On iOS 17.4+ the browser's first load of hop 2 carries the service token too.
// If Cloudflare admits that load on the token, the origin sees a valid but
// person-less assertion. Hop 2 then redirects ONCE to itself with reauth=1,
// without consuming the request: that second load is not the initial URL, so it
// carries no service token, and Cloudflare must authenticate the person (by their
// Access cookie or a fresh IdP login). Only after that does a missing identity
// become a terminal access_denied.
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
	// browser. It has to cover a full IdP sign-in (password, 2FA) in the browser,
	// and no more.
	oidcRequestTTL = 10 * time.Minute

	// oidcStoreCap is the most entries either in-memory store holds. Hop 1 is
	// reachable by anything that gets past the edge, so the parked-request store
	// must not grow without bound under a flood. When full, a new request is
	// REFUSED rather than evicting an old one: eviction would let a flood push
	// real people's in-progress sign-ins out. Human login rates are a handful an
	// hour; 1024 live entries is far beyond any legitimate peak.
	oidcStoreCap = 1024

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

// errOIDCStoreFull is returned by oidcTTLStore.put when the store is at its cap
// even after expired entries are swept.
var errOIDCStoreFull = errors.New("abs: openid store is full")

// oidcTTLStore is a bounded, single-use, expiring map. It backs both the parked
// hop-1 requests and the minted authorization codes.
//
// Everything lives in memory: a restart invalidates every in-flight sign-in,
// which is correct for credentials that live seconds to minutes, and keeps
// short-lived secrets out of the database.
type oidcTTLStore[V any] struct {
	mu      sync.Mutex
	entries map[string]oidcTTLEntry[V]
	max     int
	purgeAt time.Time
}

type oidcTTLEntry[V any] struct {
	value     V
	expiresAt time.Time
}

func newOIDCTTLStore[V any](maxEntries int) *oidcTTLStore[V] {
	return &oidcTTLStore[V]{entries: map[string]oidcTTLEntry[V]{}, max: maxEntries}
}

// put registers key until now+ttl. It refuses with errOIDCStoreFull rather than
// evicting a live entry (see oidcStoreCap).
func (s *oidcTTLStore[V]) put(key string, value V, now time.Time, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= s.max || now.Sub(s.purgeAt) > time.Minute {
		for k, e := range s.entries {
			if !now.Before(e.expiresAt) {
				delete(s.entries, k)
			}
		}
		s.purgeAt = now
	}
	if _, exists := s.entries[key]; !exists && len(s.entries) >= s.max {
		return errOIDCStoreFull
	}
	s.entries[key] = oidcTTLEntry[V]{value: value, expiresAt: now.Add(ttl)}
	return nil
}

// take returns the entry for key and removes it. It reports false for a key
// that is unknown, already taken, or expired at now.
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

// live reports whether key is present and unexpired, without consuming it.
func (s *oidcTTLStore[V]) live(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	return ok && now.Before(e.expiresAt)
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
	// Hop 1's client, recorded for the hop-2 audit line. Not enforced: Safari
	// with iCloud Private Relay legitimately reaches us from a different
	// address than the app's own URLSession.
	SourceIP  string
	UserAgent string
}

// oidcCodes is package-level because the web OAuth deep-link flow
// (internal/server/handlers) mints into it through MintAppAuthorizationCode.
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
	if err := oidcCodes.put(code, oidcPendingCode{
		UserID:      userID,
		Email:       email,
		Challenge:   challenge,
		RedirectURI: redirectURI,
	}, now, oidcCodeTTL); err != nil {
		return "", err
	}
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
// It resolves no identity. It validates redirect_uri and PKCE, parks the request
// under a random single-use id, and 302s to hop 2 on this server's own https
// origin. Every failure is a 400 with a plain-text body and no Location: the app
// shows a 400 body to the user and treats any other status as a generic error,
// and it hands any Location it receives to ASWebAuthenticationSession, which
// crashes on anything but an http(s) URL. So this hop never redirects to the
// client's custom scheme, on success or on failure.
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
	if err := h.oidcRequests.put(id, oidcPendingRequest{
		Challenge:   challenge,
		RedirectURI: redirectURI,
		State:       state,
		ClientID:    clientID,
		SourceIP:    strings.TrimSpace(c.ClientIP()),
		UserAgent:   c.Request.UserAgent(),
	}, h.now(), oidcRequestTTL); err != nil {
		oidcLog.Warn("openid authorize refused a new sign-in: %v (cap %d)", err, oidcStoreCap)
		h.oidcAuthorizeReject(c, "pending-store-full",
			"Too many sign-ins are in progress. Try again in a few minutes.")
		return
	}

	absauth.Audit(absauth.AuditEvent{
		Action: "openid-authorize", Outcome: absauth.OutcomeSuccess, Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), Reason: "browser-hop-issued",
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
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
	})
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusBadRequest, message)
	c.Abort()
}

// OpenIDBrowser handles GET /auth/openid/browser — hop 2, opened in the system
// browser by ASWebAuthenticationSession. Cloudflare Access authenticates the
// person in front of this route, so the verified assertion names them.
//
// The parked request is consumed before any terminal answer, so a req id yields
// at most one code or one error. Its redirect_uri and state come from the parked
// record, never from this request's query.
func (h *Handler) OpenIDBrowser(c *gin.Context) {
	oidcNoStore(c)
	c.Header("Referrer-Policy", "no-referrer")

	reqID := c.Query("req")
	// Check the id BEFORE resolving identity: resolution can provision a user,
	// and a junk or replayed link must not cause that.
	if !isS256Shaped(reqID) || !h.oidcRequests.live(reqID, h.now()) {
		h.oidcBrowserDead(c)
		return
	}

	identity, authErr := h.resolver.ResolveCFAssertion(c)
	noPerson := authErr == nil && (identity == nil || identity.User == nil)

	// A valid assertion that names no person: on iOS 17.4+ the initial browser
	// load carries the app's service token. Redirect to ourselves once, without
	// consuming the request; that load is not the initial URL, carries no
	// service token, and so makes Cloudflare authenticate the person.
	if noPerson && strings.TrimSpace(c.GetHeader(oauth.CFAccessHeader)) != "" && c.Query(oidcReauthParam) == "" {
		if origin, ok := oidcRequestOrigin(c); ok {
			oidcLog.Info("openid browser hop carried a non-person assertion (service_token=%s); redirecting once to shed it",
				logger.SanitizeLogValue(servermiddleware.ABSServiceToken(c)))
			c.Header("Location", oidcBrowserURL(origin, reqID, true))
			c.Status(http.StatusFound)
			return
		}
	}

	pending, ok := h.oidcRequests.take(reqID, h.now())
	if !ok {
		// Lost a race with a concurrent load of the same link, or expired since
		// the check above.
		h.oidcBrowserDead(c)
		return
	}

	ev := absauth.AuditEvent{
		Action: "openid-browser", Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), Path: c.Request.URL.Path,
		UserAgent: c.Request.UserAgent(), ServiceToken: servermiddleware.ABSServiceToken(c),
	}

	if authErr != nil {
		oidcLog.Warn("openid browser hop rejected an assertion: reason=%s status=%d email=%s hop1_ip=%s",
			authErr.Reason, authErr.Status, logger.SanitizeLogValue(authErr.Email),
			logger.SanitizeLogValue(pending.SourceIP))
		ev.Outcome = oidcOutcomeForStatus(authErr.Status)
		ev.Reason = authErr.Reason
		ev.Email = authErr.Email
		absauth.Audit(ev)
		errCode := "access_denied"
		if authErr.Status >= http.StatusInternalServerError {
			errCode = "server_error"
		}
		oidcRedirectError(c, pending.RedirectURI, pending.State, errCode, authErr.Message)
		return
	}
	if noPerson {
		// Nobody was authenticated even after the service-token bounce. In this
		// deployment that means the request reached the origin without Cloudflare
		// Access authenticating a person. Report it through the client's callback:
		// ASWebAuthenticationSession is waiting for exactly that navigation and
		// closes cleanly on it.
		oidcLog.Warn("openid browser hop resolved no verified identity: service_token=%s reauth=%t hop1_ip=%s client_id=%s",
			logger.SanitizeLogValue(ev.ServiceToken), c.Query(oidcReauthParam) != "",
			logger.SanitizeLogValue(pending.SourceIP), logger.SanitizeLogValue(pending.ClientID))
		ev.Outcome = absauth.OutcomeDenied
		ev.Reason = "no-verified-identity"
		absauth.Audit(ev)
		oidcRedirectError(c, pending.RedirectURI, pending.State, "access_denied",
			"no verified identity on this request")
		return
	}

	ev.UserID, ev.Username, ev.Email = identity.User.ID, identity.User.Username, identity.Email
	code, err := mintOIDCCode(identity.User.ID, identity.Email, pending.Challenge, pending.RedirectURI, h.now())
	if err != nil {
		oidcLog.Error("openid browser hop could not mint a code: user_id=%s: %v",
			logger.SanitizeLogValue(identity.User.ID), err)
		ev.Outcome = absauth.OutcomeError
		ev.Reason = "code-mint-error"
		absauth.Audit(ev)
		oidcRedirectError(c, pending.RedirectURI, pending.State, "server_error", "could not mint code")
		return
	}

	ev.Outcome = absauth.OutcomeSuccess
	ev.Reason = "code-issued"
	absauth.Audit(ev)
	oidcRedirect(c, pending.RedirectURI, map[string]string{"code": code, "state": pending.State})
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

// oidcRedirect 302s to target with params appended, skipping empty values.
//
// The Location header is written directly rather than via c.Redirect because the
// target is a custom scheme (audiobooth://oauth) — this keeps the value exactly
// as the client registered it. Only hop 2 calls this; hop 1 never redirects to
// the client's target.
func oidcRedirect(c *gin.Context, target string, params map[string]string) {
	c.Header("Location", buildOIDCRedirect(target, params))
	c.Status(http.StatusFound)
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
func oidcRedirectError(c *gin.Context, target, state, code, description string) {
	oidcRedirect(c, target, map[string]string{
		"error":             code,
		"error_description": description,
		"state":             state,
	})
}
