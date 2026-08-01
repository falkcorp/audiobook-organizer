// file: internal/server/handlers/abs/openid.go
// version: 1.0.0
// guid: 3b8e5a14-70c9-4f26-9d51-a2c60f7b8e93
// last-edited: 2026-08-01

package abs

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
)

// Single sign-on for Audiobookshelf clients, implemented against AudioBooth's
// actual source rather than a guess about it
// (github.com/AudioBooth/AudioBooth, MPL-2.0):
//
//	OIDCAuthenticationManager.swift  — opens ASWebAuthenticationSession at
//	  GET /auth/openid?client_id=…&response_type=code&scope=openid
//	      &redirect_uri=audiobooth://oauth&callback=audiobooth://oauth
//	      &code_challenge=<S256>&code_challenge_method=S256
//	  with callbackURLScheme "audiobooth".
//
//	AuthenticationService.loginWithOIDC — then calls
//	  GET /auth/openid/callback?code=…&code_verifier=…[&state=…]
//	  with headers = customHeaders + Cookie: <cookies from the web session>,
//	  decoding the reply as Authorize { user, userDefaultLibraryId,
//	  ereaderDevices, serverSettings } and reading user.credentials.
//
// That last type is exactly what POST /login already returns, so the callback
// hands off to issueSession and the two paths cannot drift apart.
//
// # Why this works without a second identity provider
//
// The authorize hop arrives through Cloudflare Access, which has already
// authenticated the person — verified in production, where AudioBooth's request
// carried a 1008-byte Cf-Access-Jwt-Assertion. So this endpoint does not
// redirect anywhere to establish identity; it reads the assertion already on the
// request via the same ResolveCFAssertion that POST /login uses, and mints a code.
//
// # The two hops are different clients, and that is the point
//
// The authorize hop is the system web session (it holds the Access cookie). The
// callback hop is the app's own URLSession, which does NOT inherit that cookie —
// observed directly: the callback never reached the origin at all, because
// Cloudflare answered it with 38KB of sign-in HTML and AudioBooth reported
// "Failed to decode server response". The app must therefore carry its own
// Cloudflare credential (a service token in Custom Headers, which
// AuthenticationService does attach), and that credential is a MACHINE identity
// with no email.
//
// The authorization code is what carries the human identity across that gap.
// That is ordinary OAuth, and it is why the callback deliberately does not
// require an identity assertion of its own.
const (
	// oidcCodeTTL bounds how long a minted code is redeemable. AudioBooth
	// exchanges within a second of the redirect; a minute is generous and keeps
	// a leaked redirect URL from being useful for long.
	oidcCodeTTL = 60 * time.Second

	// oidcAuthMethod labels this path in audit records and ABSIdentity.Mode.
	oidcAuthMethod = "openid"
)

// oidcPendingCode is one minted authorization code awaiting redemption.
type oidcPendingCode struct {
	UserID    string
	Email     string
	Challenge string
	ExpiresAt time.Time
}

// oidcCodeStore holds minted codes in memory. A restart invalidates everything,
// which is correct for a 60-second single-use credential and avoids putting a
// short-lived secret in the database.
type oidcCodeStore struct {
	mu      sync.Mutex
	codes   map[string]oidcPendingCode
	purgeAt time.Time
}

var oidcCodes = &oidcCodeStore{codes: map[string]oidcPendingCode{}}

// put registers a code. Callers hold no lock.
func (s *oidcCodeStore) put(code string, pending oidcPendingCode, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Opportunistic sweep; the map stays tiny at human login rates.
	if now.Sub(s.purgeAt) > time.Minute {
		for k, v := range s.codes {
			if v.ExpiresAt.Before(now) {
				delete(s.codes, k)
			}
		}
		s.purgeAt = now
	}
	s.codes[code] = pending
}

// take returns a code and removes it. SINGLE USE is enforced here rather than at
// the call site: the delete happens under the same lock as the read, so two
// concurrent redemptions of one code cannot both succeed.
func (s *oidcCodeStore) take(code string) (oidcPendingCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.codes[code]
	if ok {
		delete(s.codes, code)
	}
	return pending, ok
}

// OpenIDAuthorize handles GET /auth/openid.
//
// It never renders anything: on success it 302s to the client's redirect_uri
// with a code, and on failure it 302s to the same place with an OAuth `error`
// parameter, because a client that opened this in a web session can only get an
// answer back through its own callback scheme. Returning JSON here would leave
// AudioBooth's ASWebAuthenticationSession hanging until the user cancels.
func (h *Handler) OpenIDAuthorize(c *gin.Context) {
	// AudioBooth sends both; redirect_uri is the OAuth-standard name and wins.
	redirectURI := strings.TrimSpace(c.Query("redirect_uri"))
	if redirectURI == "" {
		redirectURI = strings.TrimSpace(c.Query("callback"))
	}
	state := c.Query("state")

	// With no redirect target there is nowhere to report anything, so this is the
	// one case that answers directly.
	if redirectURI == "" {
		respondError(c, http.StatusBadRequest, "redirect_uri is required")
		return
	}

	challenge := strings.TrimSpace(c.Query("code_challenge"))
	method := strings.TrimSpace(c.Query("code_challenge_method"))
	if challenge == "" || !strings.EqualFold(method, "S256") {
		// PKCE is mandatory, and only S256 is accepted. "plain" would let anyone
		// who observes the redirect redeem the code, which on a custom URL scheme
		// is a realistic threat: another installed app can register the same scheme.
		oidcRedirectError(c, redirectURI, state, "invalid_request",
			"code_challenge with code_challenge_method=S256 is required")
		return
	}

	// Identity comes from the Cloudflare Access assertion already on this
	// request — the same path POST /login uses. A terminal error here means the
	// assertion was present but bad; nil means there was none to act on.
	identity, authErr := h.resolver.ResolveCFAssertion(c)
	if authErr != nil {
		slog.Warn("abs: openid authorize rejected an assertion",
			"reason", authErr.Reason, "status", authErr.Status)
		oidcRedirectError(c, redirectURI, state, "access_denied", authErr.Message)
		return
	}
	if identity == nil || identity.User == nil {
		// No verified identity reached us. In this deployment that means the
		// request did not come through Cloudflare Access, so there is nobody to
		// authenticate and no password prompt we could usefully render into a
		// client's web session.
		oidcRedirectError(c, redirectURI, state, "access_denied",
			"no verified identity on this request")
		return
	}

	code, err := absauth.NewRefreshSeed() // 256-bit URL-safe random; not a refresh token
	if err != nil {
		oidcRedirectError(c, redirectURI, state, "server_error", "could not mint code")
		return
	}

	now := h.now()
	oidcCodes.put(code, oidcPendingCode{
		UserID:    identity.User.ID,
		Email:     identity.Email,
		Challenge: challenge,
		ExpiresAt: now.Add(oidcCodeTTL),
	}, now)

	absauth.Audit(absauth.AuditEvent{
		Action: "openid-authorize", Outcome: absauth.OutcomeSuccess, Mode: oidcAuthMethod,
		SourceIP: strings.TrimSpace(c.ClientIP()), UserID: identity.User.ID,
		Username: identity.User.Username, Email: identity.Email,
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})

	oidcRedirect(c, redirectURI, map[string]string{"code": code, "state": state})
}

// OpenIDCallback handles GET /auth/openid/callback — the code-for-session
// exchange, called by the app's own HTTP client.
//
// This returns JSON, not a redirect: AudioBooth decodes the body as Authorize.
// It deliberately does NOT require a Cloudflare identity assertion, because this
// hop is authenticated to the edge by a service token (a machine credential with
// no email). The code is the identity.
func (h *Handler) OpenIDCallback(c *gin.Context) {
	code := strings.TrimSpace(c.Query("code"))
	verifier := strings.TrimSpace(c.Query("code_verifier"))
	if code == "" || verifier == "" {
		respondError(c, http.StatusBadRequest, "code and code_verifier are required")
		return
	}

	pending, ok := oidcCodes.take(code) // single-use: removed even if it then fails
	if !ok {
		respondError(c, http.StatusUnauthorized, "invalid or already-used code")
		return
	}
	now := h.now()
	if now.After(pending.ExpiresAt) {
		respondError(c, http.StatusUnauthorized, "code expired")
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

// oidcRedirect 302s to target with params appended, skipping empty values.
//
// The Location header is written directly rather than via c.Redirect because the
// target is a custom scheme (audiobooth://oauth) — this keeps the value exactly
// as the client registered it.
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

// oidcRedirectError reports a failure through the client's own callback so its
// web session closes with a message instead of hanging.
func oidcRedirectError(c *gin.Context, target, state, code, description string) {
	oidcRedirect(c, target, map[string]string{
		"error":             code,
		"error_description": description,
		"state":             state,
	})
}
