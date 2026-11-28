// file: internal/server/wire_auth_routes.go
// version: 1.8.3
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-11-10

package server

import (
	"net/http"
	"time"

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
	inviteLimits := newInviteRateLimits(s.bgCtx, time.Now)

	authGroup := api.Group("/auth")
	{
		authGroup.GET("/status", authH.GetStatus)
		if s.identity != nil {
			// Under core identity the first account is core's to create (POST
			// /api/core/v1/auth/setup, with the bootstrap token core writes at
			// start); HEAD's handler guards on books' user count, which M3
			// drains, so it would serve every migrated install as a fresh one.
			// It answers 410 and reads nothing.
			authGroup.POST("/setup", gone("core's POST /api/core/v1/auth/setup"))
		} else {
			authGroup.POST("/setup", authH.SetupInitialAdmin)
		}
		authGroup.POST("/login", s.loginCeilingMiddleware(nil, respondWebLoginError), authH.Login)
		if s.identity != nil {
			// Under core identity an invite is core's record: accepting and looking
			// one up are core's routes (POST /auth/invites/accept and /lookup), and
			// these two would read and write books' own, now unused, invite store.
			// They answer 410 and read no token, so they need no limiter.
			replacement := "core's invitation routes (POST /auth/invites/lookup and POST /auth/invites/accept)"
			authGroup.POST("/accept-invite", gone(replacement))
			authGroup.GET("/invite/:token", gone(replacement))
		} else {
			authGroup.POST("/accept-invite", inviteLimits.middleware(inviteTokenFromBody), s.handleAcceptInvite)
			authGroup.GET("/invite/:token", inviteLimits.middleware(inviteTokenFromParam), userH.PublicInvite)
		}
		authGroup.POST("/bootstrap", s.handleBootstrap)
		// OAuth2 / OIDC SSO — public: /start redirects to the IdP, /callback completes
		// the handshake and starts a session. Both are no-ops for unconfigured providers.
		if oauthH != nil {
			authGroup.GET("/oauth-providers", oauthH.Providers)
			// Under core identity core serves these two exact paths on its fixed
			// group (app.Options.Auth.OIDCCallbackBase), so gin does not.
			if !s.servedByCore(http.MethodGet, "/api/v1/auth/oauth/:provider/start") {
				authGroup.GET("/oauth/:provider/start", oauthH.Start)
			}
			if !s.servedByCore(http.MethodGet, "/api/v1/auth/oauth/:provider/callback") {
				authGroup.GET("/oauth/:provider/callback", oauthH.Callback)
			}
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
		// The sessions list returns revoke handles, never session IDs (the ID is the bearer token; 05-authn §11.1 item 1); DELETE /sessions/:id takes a handle.
		authProtected.GET("/sessions", authH.ListMySessions)
		authProtected.DELETE("/sessions/:id", authH.RevokeMySession)
		s.credRoute(authProtected, http.MethodPut, "/me/password", authH.ChangePassword)
		// The guest's one-time phone-app key under core identity: the page calls it
		// right after core's accept with the session the accept gave it
		// (guest_key_claim.go). A credRoute because it hands out a credential, so
		// an API key or ABS token is refused; the delegated session of an accept
		// passes.
		s.credRoute(authProtected, http.MethodPost, "/phone-key/claim", s.handleClaimGuestKey)
		// Deleted under core identity (routesDeletedAtTheFlip): core's POST
		// /api/core/v1/users/{id}/temp-login mints the link.
		if !s.deletedAtTheFlip(http.MethodPost, "/api/v1/auth/temp-tokens") {
			s.credRoute(authProtected, http.MethodPost, "/temp-tokens", s.perm(permTempLoginMint()), s.createTempLoginToken)
		}

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
		// key and revoke the old one instead. Under core identity the
		// conditional credential guard is a pass-through (credGuardMiddleware,
		// since B10g), so the stub answers 410 to every caller and mints
		// nothing; apiKeyOwnedByAnotherUser is consulted only by the pre-identity
		// guard.
		s.credRouteWhen(authProtected, http.MethodPost, "/api-keys/:id/rotate", s.apiKeyOwnedByAnotherUser,
			gone("POST /api/v1/auth/api-keys (create a new key), then DELETE /api/v1/auth/api-keys/:id (revoke the old one)"))
	}
}
