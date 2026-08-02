// file: internal/server/handlers/oauth_login.go
// version: 1.1.0
// guid: 2e9c0b47-6a31-4d58-8f04-1b5a7c2e9d63
// last-edited: 2026-08-02

package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// oauthStateCookie carries the signed CSRF-state + PKCE blob across the IdP redirect.
// It MUST be SameSite=Lax (not Strict): the callback arrives as a top-level navigation
// from the IdP's domain, and a Strict cookie would be dropped on that cross-site hop.
const oauthStateCookie = "oauth_state"

// OAuthHandler serves the OAuth2/OIDC login endpoints. Providers are pre-built at wire
// time (Google needs OIDC discovery), keyed by provider name.
type OAuthHandler struct {
	store     database.Store
	cfg       *oauth.Config
	codec     *oauth.StateCodec
	providers map[string]oauth.Provider
}

// NewOAuthHandler constructs the handler. providers may be empty (all disabled).
func NewOAuthHandler(store database.Store, cfg *oauth.Config, codec *oauth.StateCodec, providers map[string]oauth.Provider) *OAuthHandler {
	return &OAuthHandler{store: store, cfg: cfg, codec: codec, providers: providers}
}

// Providers handles GET /auth/oauth/providers — the public list of enabled provider
// names, so the login page renders only the buttons that will actually work.
func (h *OAuthHandler) Providers(c *gin.Context) {
	names := make([]string, 0, len(h.providers))
	for name := range h.providers {
		names = append(names, name)
	}
	httputil.RespondWithOK(c, gin.H{"providers": names})
}

// Start handles GET /auth/oauth/:provider/start — it mints CSRF state + a PKCE
// verifier, stashes them in a signed short-lived cookie, and redirects to the IdP.
func (h *OAuthHandler) Start(c *gin.Context) {
	provider := strings.ToLower(c.Param("provider"))
	p, ok := h.providers[provider]
	if !ok || h.cfg == nil || !h.cfg.ProviderEnabled(provider) {
		httputil.RespondWithNotFound(c, "oauth provider", provider)
		return
	}
	redirectURI, err := h.cfg.RedirectURI(provider)
	if err != nil {
		httputil.RespondWithInternalError(c, "oauth redirect URI not configured")
		return
	}
	state, err := oauth.GenerateState()
	if err != nil {
		httputil.RespondWithInternalError(c, "oauth state")
		return
	}
	verifier, err := oauth.GenerateCodeVerifier()
	if err != nil {
		httputil.RespondWithInternalError(c, "oauth verifier")
		return
	}
	app, ok := appDeepLinkParams(c)
	if !ok {
		return // appDeepLinkParams already answered
	}

	blob, err := h.codec.Encode(oauth.StatePayload{
		State: state, Verifier: verifier, Provider: provider, Return: sanitizeReturn(c.Query("return")),
		// Empty unless the caller is a registered native client (see
		// appDeepLinkParams). The two PKCE exchanges are kept apart here: Verifier
		// above is ours for the IdP hop, AppChallenge is the client's for its own.
		AppRedirectURI: app.redirectURI, AppChallenge: app.challenge, AppState: app.state,
	})
	if err != nil {
		httputil.RespondWithInternalError(c, "oauth state encode")
		return
	}
	setOAuthStateCookie(c, blob)
	http.Redirect(c.Writer, c.Request, p.AuthCodeURL(state, oauth.CodeChallengeS256(verifier), redirectURI), http.StatusFound)
}

// Callback handles GET /auth/oauth/:provider/callback — it verifies the CSRF state,
// exchanges the code, resolves/creates the (allowlisted) user, and starts a session.
func (h *OAuthHandler) Callback(c *gin.Context) {
	provider := strings.ToLower(c.Param("provider"))
	p, ok := h.providers[provider]
	if !ok {
		httputil.RespondWithNotFound(c, "oauth provider", provider)
		return
	}
	// The IdP may report a user-declined / error result.
	if e := c.Query("error"); e != "" {
		redirectToLogin(c, "oauth_denied")
		return
	}
	// Verify the signed state cookie and CSRF match.
	rawBlob, err := c.Cookie(oauthStateCookie)
	if err != nil {
		redirectToLogin(c, "oauth_state_missing")
		return
	}
	payload, err := h.codec.Decode(rawBlob)
	clearOAuthStateCookie(c)
	if err != nil || payload.Provider != provider || payload.State == "" || payload.State != c.Query("state") {
		redirectToLogin(c, "oauth_state_invalid")
		return
	}
	code := c.Query("code")
	if code == "" {
		redirectToLogin(c, "oauth_no_code")
		return
	}
	redirectURI, err := h.cfg.RedirectURI(provider)
	if err != nil {
		httputil.RespondWithInternalError(c, "oauth redirect URI not configured")
		return
	}
	claims, err := p.Exchange(c.Request.Context(), code, payload.Verifier, redirectURI)
	if err != nil {
		redirectToLogin(c, "oauth_exchange_failed")
		return
	}
	user, err := h.cfg.ResolveUser(h.store, *claims)
	if err != nil {
		if errors.Is(err, oauth.ErrEmailNotAllowed) || errors.Is(err, oauth.ErrEmailNotVerified) {
			redirectToLogin(c, "oauth_not_authorized")
			return
		}
		httputil.RespondWithInternalError(c, "oauth resolve user")
		return
	}
	// A native client gets an authorization code on its own URL scheme instead of a
	// browser session. Deliberately BEFORE CreateSession: the caller is an
	// ASWebAuthenticationSession whose cookie jar is discarded the moment it closes,
	// so issuing a browser session here would leave an unusable row in the store on
	// every app login. The app's real session comes from redeeming the code.
	if payload.AppRedirectURI != "" && payload.AppChallenge != "" {
		code, mintErr := abs.MintAppAuthorizationCode(user.ID, claims.Email, payload.AppChallenge)
		if mintErr != nil {
			redirectToLogin(c, "oauth_code_mint_failed")
			return
		}
		// The target already passed the exact-match allowlist at Start and travelled
		// inside an HMAC-signed blob, so it cannot have been swapped in between.
		http.Redirect(c.Writer, c.Request,
			abs.AppRedirectURL(payload.AppRedirectURI, code, payload.AppState), http.StatusFound)
		return
	}

	session, err := h.store.CreateSession(
		user.ID,
		strings.TrimSpace(c.ClientIP()),
		strings.TrimSpace(c.Request.UserAgent()),
		defaultSessionTTL,
	)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to create session")
		return
	}
	SetSessionCookie(c, session.ID, session.ExpiresAt)
	dest := "/"
	if payload.Return != "" {
		dest = payload.Return
	}
	http.Redirect(c.Writer, c.Request, dest, http.StatusFound)
}

// appDeepLink is the native-client half of a /auth/oauth/:provider/start request.
type appDeepLink struct {
	redirectURI string
	challenge   string
	state       string
}

// appDeepLinkParams decides whether this Start request is a native-client deep link,
// answering the request itself and reporting false when it is malformed.
//
// 🔴 THE GATE IS "BOTH PRESENT", AND THAT IS A SECURITY PROPERTY, NOT TIDINESS.
// /auth/oauth/:provider/start is the UNAUTHENTICATED web login endpoint that the SPA's
// buttons hit. If a bare redirect_uri could trigger a 400, anyone could break web
// login for everyone by getting a query parameter appended to a shared link. So a
// request carrying only one of the two is treated as an ordinary web login and the
// stray parameter is ignored — no error, no behaviour change.
//
// Once BOTH are present the caller has unambiguously asked for the native flow, and
// from there every failure is a hard 400: the target must be on the exact-match
// allowlist and the method must be S256. "plain" is refused because on a custom URL
// scheme another installed app can register the same scheme and observe the redirect,
// so a plain challenge protects nothing.
func appDeepLinkParams(c *gin.Context) (appDeepLink, bool) {
	// redirect_uri is the OAuth-standard name; `callback` is the alias AudioBooth
	// also sends on /auth/openid. Accepted here so one client does not need two
	// spellings across two endpoints.
	redirectURI := strings.TrimSpace(c.Query("redirect_uri"))
	if redirectURI == "" {
		redirectURI = strings.TrimSpace(c.Query("callback"))
	}
	challenge := strings.TrimSpace(c.Query("code_challenge"))

	if redirectURI == "" || challenge == "" {
		// Not a deep-link request (or not a well-formed one). Ordinary web login.
		return appDeepLink{}, true
	}
	if !abs.AppRedirectURIAllowed(redirectURI) {
		// Answered INLINE, never by redirecting to the rejected target — bouncing to
		// an unvalidated URI to report that it is unvalidated is still an open
		// redirect. This is the control that makes account takeover impossible here.
		httputil.RespondWithBadRequest(c, "redirect_uri is not registered")
		return appDeepLink{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(c.Query("code_challenge_method")), "S256") {
		httputil.RespondWithBadRequest(c, "code_challenge_method must be S256")
		return appDeepLink{}, false
	}
	return appDeepLink{redirectURI: redirectURI, challenge: challenge, state: c.Query("state")}, true
}

// setOAuthStateCookie writes the signed state blob (SameSite=Lax, ~10 min).
func setOAuthStateCookie(c *gin.Context, blob string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    blob,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   isHTTPSRequest(c),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearOAuthStateCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: oauthStateCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isHTTPSRequest(c), SameSite: http.SameSiteLaxMode,
	})
}

// redirectToLogin bounces back to the SPA login page with a short error code the
// frontend can surface, without leaking internals.
func redirectToLogin(c *gin.Context, reason string) {
	http.Redirect(c.Writer, c.Request, "/login?error="+reason, http.StatusFound)
}

// sanitizeReturn only allows a same-site absolute path (prevents open-redirect). It
// must be a single leading slash: reject "//host" and "/\host" (browsers normalize
// backslashes to slashes, so "/\evil.com" is protocol-relative to evil.com), and any
// path containing a backslash.
func sanitizeReturn(ret string) string {
	if ret == "" || !strings.HasPrefix(ret, "/") {
		return ""
	}
	if strings.ContainsRune(ret, '\\') {
		return ""
	}
	if len(ret) > 1 && (ret[1] == '/' || ret[1] == '\\') {
		return ""
	}
	return ret
}
