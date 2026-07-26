// file: internal/server/wire_auth_routes.go
// version: 1.1.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-26

package server

import (
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
		authProtected.GET("/me", authH.Me)
		authProtected.PATCH("/me", authH.UpdateMe)
		authProtected.POST("/logout", authH.Logout)
		authProtected.GET("/sessions", authH.ListMySessions)
		authProtected.DELETE("/sessions/:id", authH.RevokeMySession)
		authProtected.PUT("/me/password", authH.ChangePassword)
		authProtected.POST("/temp-tokens", s.perm(permTempLoginMint()), s.createTempLoginToken)

		authProtected.POST("/api-keys", apiKeyH.Create)
		authProtected.GET("/api-keys", apiKeyH.List)
		authProtected.GET("/api-keys/:id", apiKeyH.Get)
		authProtected.PATCH("/api-keys/:id", apiKeyH.UpdateStatus)
		authProtected.DELETE("/api-keys/:id", apiKeyH.Revoke)
		authProtected.POST("/api-keys/:id/rotate", apiKeyH.Rotate)
	}
}
