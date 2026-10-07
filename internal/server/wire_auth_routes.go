// file: internal/server/wire_auth_routes.go
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-10-07

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
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
) {
	authGroup := api.Group("/auth")
	{
		authGroup.GET("/status", authH.GetStatus)
		authGroup.POST("/setup", authH.SetupInitialAdmin)
		authGroup.POST("/login", authH.Login)
		authGroup.POST("/accept-invite", s.handleAcceptInvite)
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
		// s.credGuard() refuses an API key (any key, any scope) on routes that
		// change credentials or identity: without it an admin key could
		// reset or mint a sign-in for an admin and hold an interactive
		// session. Inventory and reasons:
		// docs/plans/2026-10-07-apikey-expiry-and-privilege.md.
		authProtected.GET("/me", authH.Me)
		// Email is an identity: oauth.ResolveUser links an SSO login to the
		// user whose email it verifies.
		authProtected.PATCH("/me", s.credGuard(), authH.UpdateMe)
		authProtected.POST("/logout", authH.Logout)
		authProtected.GET("/sessions", authH.ListMySessions)
		authProtected.DELETE("/sessions/:id", authH.RevokeMySession)
		authProtected.PUT("/me/password", s.credGuard(), authH.ChangePassword)
		authProtected.POST("/temp-tokens", s.credGuard(), s.perm(permTempLoginMint()), s.createTempLoginToken)

		// Create and Rotate refuse a key acting on ANOTHER user's keys in the
		// handler; a key managing its own user's keys stays allowed.
		authProtected.POST("/api-keys", apiKeyH.Create)
		authProtected.GET("/api-keys", apiKeyH.List)
		authProtected.GET("/api-keys/:id", apiKeyH.Get)
		authProtected.PATCH("/api-keys/:id", apiKeyH.UpdateStatus)
		authProtected.DELETE("/api-keys/:id", apiKeyH.Revoke)
		authProtected.POST("/api-keys/:id/rotate", apiKeyH.Rotate)
	}
}

// credGuard refuses credential and identity changes to any auth method that
// may not make them (servermiddleware.RequireCredentialChangeMethod). A no-op
// when auth is disabled, exactly like s.perm: every request is then anonymous
// with full access and there is no API key to tell apart.
func (s *Server) credGuard() gin.HandlerFunc {
	if !config.AppConfig.EnableAuth {
		return func(c *gin.Context) { c.Next() }
	}
	return servermiddleware.RequireCredentialChangeMethod()
}
