// file: internal/server/handlers/oauth_login.go
// version: 1.0.0
// guid: 2e9c0b47-6a31-4d58-8f04-1b5a7c2e9d63
// last-edited: 2026-07-26

package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
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
	blob, err := h.codec.Encode(oauth.StatePayload{
		State: state, Verifier: verifier, Provider: provider, Return: sanitizeReturn(c.Query("return")),
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

// sanitizeReturn only allows a same-site absolute path (prevents open-redirect).
func sanitizeReturn(ret string) string {
	if ret == "" || !strings.HasPrefix(ret, "/") || strings.HasPrefix(ret, "//") {
		return ""
	}
	return ret
}
