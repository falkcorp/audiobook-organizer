// file: internal/server/wire_oauth.go
// version: 1.5.1
// guid: 5c2e8b04-7a19-4d63-8f05-3b6a0c9e2d47
// last-edited: 2026-11-11

package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// cfAccessVerifierOverride, when set, replaces Cloudflare's JWT verifier in
// the Access middleware. Set only by tests in this package (before
// NewServer), so the real router can be driven with Access identities
// without Cloudflare's keys; production never assigns it.
var cfAccessVerifierOverride cfaccess.CFAssertionVerifier

// buildOAuthWiring constructs the OAuth login handler and the Cloudflare Access
// middleware from config. Returns (nil-safe handler, cfMiddleware-or-nil,
// recoveryMiddleware-or-nil). The recovery middleware is the web UI's
// service-token browser recovery (cfaccess.CFServiceTokenRecovery) built
// on the same verifier as cfMW, so it exists exactly when cfMW does; it belongs on
// the SPA page routes, not the API. Nothing is enabled unless configured, and a
// provider/verifier that fails to initialize is logged and skipped rather than
// aborting startup.
func (s *Server) buildOAuthWiring() (*handlers.OAuthHandler, gin.HandlerFunc, gin.HandlerFunc) {
	// Read OAuth/Cloudflare-Access settings from config like every other subsystem.
	// config.AppConfig already holds the environment's OAuth values (a systemd
	// Environment= value reaches it through InitConfig, or core config's env
	// layer once the flip is on). Snapshot() reads under the config lock.
	cfgSnap := config.Snapshot()
	redirectBase := cfgSnap.OAuthRedirectBaseURL
	if redirectBase == "" {
		redirectBase = s.externalURL
	}
	cfg := legacyoauth.New(legacyoauth.Config{
		Enabled:            cfgSnap.OAuthEnabled,
		GitHubClientID:     cfgSnap.OAuthGithubClientID,
		GitHubClientSecret: cfgSnap.OAuthGithubClientSecret,
		GoogleClientID:     cfgSnap.OAuthGoogleClientID,
		GoogleClientSecret: cfgSnap.OAuthGoogleClientSecret,
		RedirectBaseURL:    redirectBase,
		AllowedEmails:      legacyoauth.ParseAllowedEmails(cfgSnap.OAuthAllowedEmails),
		DefaultRole:        cfgSnap.OAuthDefaultRole,
		CFAccessTeamDomain: cfgSnap.CFAccessTeamDomain,
		CFAccessAUD:        cfgSnap.CFAccessAUD,
		ExtraAllowed:       inviteGrantAllowed(s.storeForWiring()),
		OnEmailDisplaced:   s.recordEmailDisplaced,
	})

	codec, err := legacyoauth.NewStateCodec(10 * time.Minute)
	if err != nil {
		slog.Error("oauth: state codec init failed — OAuth login disabled", "err", err)
		return nil, nil, nil
	}

	// Google OIDC discovery is a synchronous network call; give it a bounded context.
	discoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	providers := map[string]legacyoauth.Provider{}
	if cfg.ProviderEnabled(legacyoauth.ProviderGitHub) {
		providers[legacyoauth.ProviderGitHub] = legacyoauth.NewGitHubProvider(cfg.GitHubClientID, cfg.GitHubClientSecret)
		slog.Info("oauth: GitHub login enabled")
	}
	if cfg.ProviderEnabled(legacyoauth.ProviderGoogle) {
		if g, gerr := legacyoauth.NewGoogleProvider(discoveryCtx, cfg.GoogleClientID, cfg.GoogleClientSecret); gerr == nil {
			providers[legacyoauth.ProviderGoogle] = g
			slog.Info("oauth: Google login enabled")
		} else {
			slog.Error("oauth: Google OIDC discovery failed — Google login disabled", "err", gerr)
		}
	}
	if len(cfg.AllowedEmails) == 0 && (len(providers) > 0 || cfg.CFAccessTeamDomain != "") {
		slog.Warn("oauth: OAUTH_ALLOWED_EMAILS is empty — every OAuth/Access login will be rejected until it is set")
	}

	oauthH := handlers.NewOAuthHandler(s.storeForWiring(), cfg, codec, providers)

	// Cloudflare Access middleware. NewCFAccessVerifier uses a lazy remote keyset that
	// refreshes in the BACKGROUND, so it must get a long-lived context, not the
	// discovery timeout context above.
	var cfMW, cfRecoveryMW gin.HandlerFunc
	if cfAccessVerifierOverride != nil {
		cfMW = s.accessMiddleware(cfAccessVerifierOverride, cfg)
		cfRecoveryMW = newCFRecoveryMiddleware(cfAccessVerifierOverride)
		s.loginCeilingExempt = newAccessExemption(cfAccessVerifierOverride, cfg)
	} else if cfg.CFAccessTeamDomain != "" && cfg.CFAccessAUD != "" {
		if v, verr := cfaccess.NewCFAccessVerifier(context.Background(), cfg.CFAccessTeamDomain, cfg.CFAccessAUD); verr == nil {
			cfMW = s.accessMiddleware(v, cfg)
			cfRecoveryMW = newCFRecoveryMiddleware(v)
			s.loginCeilingExempt = newAccessExemption(v, cfg)
			slog.Info("oauth: Cloudflare Access identity passthrough enabled", "team", cfg.CFAccessTeamDomain)
		} else {
			slog.Error("oauth: Cloudflare Access verifier init failed — passthrough disabled", "err", verr)
		}
	}

	return oauthH, cfMW, cfRecoveryMW
}

// accessMiddleware is the gin chain's Cloudflare Access stage over v (cfMW).
// Without core identity it is HEAD's binder: a verified, allow-listed identity
// is resolved to a books user (linked or created) and bound for RequireAuth.
// Under core identity it is the verification alone
// (cfaccess.CloudflareAccessVerifyOnly): core's Access extension authenticates
// the request at stage 12 and the bridge (requireAuth) reads only the principal
// it bound, and the families HEAD's resolver reads and writes are retired, so
// the binder could only attempt a refused write. The verified marker, which
// core does not set and trustedClientIP reads, is still set. Neither form
// refuses a request; the auth stage after it does.
func (s *Server) accessMiddleware(v cfaccess.CFAssertionVerifier, cfg *legacyoauth.Config) gin.HandlerFunc {
	if s.identity != nil {
		return cfaccess.CloudflareAccessVerifyOnly(v, servermiddleware.KeyPresented)
	}
	return cfaccess.CloudflareAccessAuth(
		cfaccess.NewCFAccessAuthenticatorWithVerifier(v, cfg, s.storeForWiring(), servermiddleware.CFAccessHooks(s.storeForWiring())))
}
