// file: internal/server/wire_auth_routes.go
// version: 1.7.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-10-21

package server

import (
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
)

// wireAuthRoutes registers the /auth group (public + protected) routes.
// Handler instantiation stays in wireHandlers; this method only registers.
func (s *Server) wireAuthRoutes(
	api *gin.RouterGroup,
	authMiddleware gin.HandlerFunc,
	authH *handlers.AuthHandler,
	apiKeyH *handlers.APIKeyHandler,
	oauthH *handlers.OAuthHandler,
	userH *handlers.UserHandler,
) {
	// The two public invite endpoints are bounded per token, per client and
	// globally (see inviteRateLimits): the lookup says whether a token is
	// live, and accept answers differently for missing, used and expired
	// tokens.
	inviteLimits := newInviteRateLimits(s.bgCtx)

	authGroup := api.Group("/auth")
	{
		authGroup.GET("/status", authH.GetStatus)
		authGroup.POST("/setup", authH.SetupInitialAdmin)
		authGroup.POST("/login", s.loginCeiling().middleware(nil), authH.Login)
		authGroup.POST("/accept-invite", inviteLimits.middleware(inviteTokenFromBody), s.handleAcceptInvite)
		authGroup.GET("/invite/:token", inviteLimits.middleware(inviteTokenFromParam), userH.PublicInvite)
		authGroup.POST("/bootstrap", s.handleBootstrap)
		// OAuth2 / OIDC SSO — public: /start redirects to the IdP, /callback completes
		// the handshake and starts a session. Both are no-ops for unconfigured providers.
		if oauthH != nil {
			authGroup.GET("/oauth-providers", oauthH.Providers)
			authGroup.GET("/oauth/:provider/start", oauthH.Start)
			authGroup.GET("/oauth/:provider/callback", oauthH.Callback)
		}
	}

	authProtected := authGroup.Group("")
	authProtected.Use(authMiddleware)
	{
		// s.credRoute refuses an API key (any key, any scope) on routes that
		// change credentials or identity: without it an admin key could
		// reset or mint a sign-in for an admin and hold an interactive
		// session. Inventory and reasons:
		// docs/plans/2026-10-07-apikey-expiry-and-privilege.md.
		authProtected.GET("/me", authH.Me)
		// Email is an identity: oauth.ResolveUser links an SSO login to the
		// user whose email it verifies.
		s.credRoute(authProtected, http.MethodPatch, "/me", authH.UpdateMe)
		authProtected.POST("/logout", authH.Logout)
		authProtected.GET("/sessions", authH.ListMySessions)
		authProtected.DELETE("/sessions/:id", authH.RevokeMySession)
		s.credRoute(authProtected, http.MethodPut, "/me/password", authH.ChangePassword)
		s.credRoute(authProtected, http.MethodPost, "/temp-tokens", s.perm(permTempLoginMint()), s.createTempLoginToken)

		// A key may manage its own user's keys (clamped to its own expiry);
		// creating a key for ANOTHER user mints a credential for them and is
		// refused at the route. Re-enabling an inactive key is handing a
		// credential back, so PATCH is guarded whoever owns it.
		s.credRouteWhen(authProtected, http.MethodPost, "/api-keys", apiKeyCreateForAnotherUser, apiKeyH.Create)
		authProtected.GET("/api-keys", apiKeyH.List)
		authProtected.GET("/api-keys/:id", apiKeyH.Get)
		s.credRoute(authProtected, http.MethodPatch, "/api-keys/:id", apiKeyH.UpdateStatus)
		authProtected.DELETE("/api-keys/:id", apiKeyH.Revoke)
		// POST /api-keys/:id/rotate was retired by 01-P81k: it answers 410 Gone
		// for one release (owner decision D9) and is then removed. Create a new
		// key and revoke the old one instead. The route keeps its credential
		// guard, so the auth middleware and the refusal of an API key rotating
		// another user's key still run before the 410.
		s.credRouteWhen(authProtected, http.MethodPost, "/api-keys/:id/rotate", s.apiKeyOwnedByAnotherUser,
			gone("POST /api/v1/auth/api-keys (create a new key), then DELETE /api/v1/auth/api-keys/:id (revoke the old one)"))
	}
}
