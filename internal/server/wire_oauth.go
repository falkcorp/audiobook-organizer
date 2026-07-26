// file: internal/server/wire_oauth.go
// version: 1.0.0
// guid: 5c2e8b04-7a19-4d63-8f05-3b6a0c9e2d47
// last-edited: 2026-07-26

package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// buildOAuthWiring constructs the OAuth login handler and the Cloudflare Access
// middleware from config. Returns (nil-safe handler, cfMiddleware-or-nil). Nothing is
// enabled unless configured, and a provider/verifier that fails to initialize is
// logged and skipped rather than aborting startup.
func (s *Server) buildOAuthWiring() (*handlers.OAuthHandler, gin.HandlerFunc) {
	redirectBase := config.AppConfig.OAuthRedirectBaseURL
	if redirectBase == "" {
		redirectBase = s.externalURL
	}
	cfg := oauth.New(oauth.Config{
		Enabled:            config.AppConfig.OAuthEnabled,
		GitHubClientID:     config.AppConfig.OAuthGithubClientID,
		GitHubClientSecret: config.AppConfig.OAuthGithubClientSecret,
		GoogleClientID:     config.AppConfig.OAuthGoogleClientID,
		GoogleClientSecret: config.AppConfig.OAuthGoogleClientSecret,
		RedirectBaseURL:    redirectBase,
		AllowedEmails:      oauth.ParseAllowedEmails(config.AppConfig.OAuthAllowedEmails),
		DefaultRole:        config.AppConfig.OAuthDefaultRole,
		CFAccessTeamDomain: config.AppConfig.CFAccessTeamDomain,
		CFAccessAUD:        config.AppConfig.CFAccessAUD,
	})

	codec, err := oauth.NewStateCodec(10 * time.Minute)
	if err != nil {
		slog.Error("oauth: state codec init failed — OAuth login disabled", "err", err)
		return nil, nil
	}

	// Google OIDC discovery is a synchronous network call; give it a bounded context.
	discoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	providers := map[string]oauth.Provider{}
	if cfg.ProviderEnabled(oauth.ProviderGitHub) {
		providers[oauth.ProviderGitHub] = oauth.NewGitHubProvider(cfg.GitHubClientID, cfg.GitHubClientSecret)
		slog.Info("oauth: GitHub login enabled")
	}
	if cfg.ProviderEnabled(oauth.ProviderGoogle) {
		if g, gerr := oauth.NewGoogleProvider(discoveryCtx, cfg.GoogleClientID, cfg.GoogleClientSecret); gerr == nil {
			providers[oauth.ProviderGoogle] = g
			slog.Info("oauth: Google login enabled")
		} else {
			slog.Error("oauth: Google OIDC discovery failed — Google login disabled", "err", gerr)
		}
	}
	if len(cfg.AllowedEmails) == 0 && (len(providers) > 0 || cfg.CFAccessTeamDomain != "") {
		slog.Warn("oauth: OAUTH_ALLOWED_EMAILS is empty — every OAuth/Access login will be rejected until it is set")
	}

	oauthH := handlers.NewOAuthHandler(s.Store(), cfg, codec, providers)

	// Cloudflare Access middleware. NewCFAccessVerifier uses a lazy remote keyset that
	// refreshes in the BACKGROUND, so it must get a long-lived context, not the
	// discovery timeout context above.
	var cfMW gin.HandlerFunc
	if cfg.CFAccessTeamDomain != "" && cfg.CFAccessAUD != "" {
		if v, verr := oauth.NewCFAccessVerifier(context.Background(), cfg.CFAccessTeamDomain, cfg.CFAccessAUD); verr == nil {
			cfMW = servermiddleware.CloudflareAccessAuth(
				servermiddleware.NewCFAccessAuthenticator(v, cfg, s.Store()))
			slog.Info("oauth: Cloudflare Access identity passthrough enabled", "team", cfg.CFAccessTeamDomain)
		} else {
			slog.Error("oauth: Cloudflare Access verifier init failed — passthrough disabled", "err", verr)
		}
	}

	return oauthH, cfMW
}
