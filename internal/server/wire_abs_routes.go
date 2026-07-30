// file: internal/server/wire_abs_routes.go
// version: 1.0.0
// guid: 9c6b13f8-40a2-4e57-b18d-72e0a5c4d396
// last-edited: 2026-07-30

package server

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// absReservedPaths are the exact top-level paths the Audiobookshelf-compatible surface
// owns under /api/. They must be EXCLUDED from the global /api/* → /api/v1/* redirect
// in setupRoutes: without the exclusion, GET /api/me 301s to /api/v1/me and the ABS
// clients follow the redirect into the app API, which answers a completely different
// shape (or 401s) — the endpoint would look implemented and behave broken.
//
// Kept as an explicit list rather than a broad prefix so adding an ABS endpoint is a
// deliberate act, and so a future /api/v1 route can never be captured by accident.
var absReservedPaths = []string{
	"/api/me",
}

// absReservedPathPrefixes covers ABS sub-trees (e.g. /api/me/sessions/:id).
var absReservedPathPrefixes = []string{
	"/api/me/",
}

// absReservedPath reports whether a request path belongs to the ABS surface and must
// therefore skip the /api/* → /api/v1/* compatibility redirect.
func absReservedPath(path string) bool {
	for _, p := range absReservedPaths {
		if path == p {
			return true
		}
	}
	for _, prefix := range absReservedPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// wireABSRoutes builds and registers the Audiobookshelf-compatible router group.
//
// It is a NEW TOP-LEVEL GROUP, not a child of /api/v1 (spec §3.6), with its own
// fail-closed identity middleware. Nothing about the existing auth paths changes: the
// `abk_` API-key scheme, browser `sess:` sessions, OAuth, and the fail-open
// Cloudflare-Access middleware on /api/v1 all keep working untouched.
//
// FAIL-CLOSED AT BOOT: if the ABS API is enabled but its configuration is invalid —
// most importantly a missing or too-short ABS_JWT_SECRET — the process exits rather
// than starting a partially-wired auth surface. Starting with a generated secret would
// invalidate every client token on each restart; starting with the surface silently
// disabled would be worse, because the operator would think it was live.
//
// When the flag is off (the default) this registers nothing and returns immediately.
func (s *Server) wireABSRoutes() {
	snap := config.Snapshot()
	if !snap.ABSAPIEnabled {
		return
	}

	cfg, err := absauth.Load(absauth.Settings{
		Enabled:          true,
		AuthModes:        snap.ABSAuthModes,
		JWTSecret:        snap.ABSJWTSecret,
		AccessTokenTTL:   snap.ABSAccessTokenTTL,
		RefreshTokenTTL:  snap.ABSRefreshTokenTTL,
		RefreshGrace:     snap.ABSRefreshGrace,
		ServerVersion:    snap.ABSServerVersion,
		DefaultLibraryID: snap.ABSDefaultLibraryID,
	})
	if err != nil {
		slog.Error("abs: refusing to start — the Audiobookshelf API is enabled but misconfigured", "err", err)
		os.Exit(1)
	}

	// Cloudflare-Access verifier for resolver step 1. It is built independently of
	// buildOAuthWiring's verifier so the ABS group is not coupled to the /api/v1
	// passthrough, and with context.Background() because the remote keyset refreshes
	// lazily in the background as Cloudflare rotates signing keys.
	var verifier servermiddleware.CFAssertionVerifier
	oauthCfg := oauth.New(oauth.Config{
		AllowedEmails: oauth.ParseAllowedEmails(snap.OAuthAllowedEmails),
		DefaultRole:   snap.OAuthDefaultRole,
	})
	if cfg.Modes.CF {
		if snap.CFAccessTeamDomain == "" || snap.CFAccessAUD == "" {
			// Not fatal when the JWT resolver can still serve requests, but it IS fatal
			// when cf is the only enabled mode: nothing could ever authenticate.
			if !cfg.Modes.JWT {
				slog.Error("abs: refusing to start — ABS_AUTH_MODES=cf requires CF_ACCESS_TEAM_DOMAIN and CF_ACCESS_AUD; " +
					"without them no request could ever authenticate")
				os.Exit(1)
			}
			slog.Warn("abs: Cloudflare Access mode is enabled but CF_ACCESS_TEAM_DOMAIN/CF_ACCESS_AUD are unset — " +
				"assertions cannot be verified, so only the JWT resolver will authenticate")
		} else if v, verr := oauth.NewCFAccessVerifier(context.Background(), snap.CFAccessTeamDomain, snap.CFAccessAUD); verr == nil {
			verifier = v
			if len(oauthCfg.AllowedEmails) == 0 {
				// Fail-closed consequence worth shouting about: with an empty allowlist
				// every verified identity is a 403 and nothing is ever provisioned.
				slog.Warn("abs: OAUTH_ALLOWED_EMAILS is empty — every Cloudflare-Access identity will be rejected with 403 " +
					"and no user will be provisioned")
			}
		} else {
			if !cfg.Modes.JWT {
				slog.Error("abs: refusing to start — Cloudflare Access verifier init failed and cf is the only enabled auth mode", "err", verr)
				os.Exit(1)
			}
			slog.Error("abs: Cloudflare Access verifier init failed — only the JWT resolver will authenticate", "err", verr)
		}
	}

	absStore, ok := s.Store().(abshandler.Store)
	if !ok {
		slog.Error("abs: refusing to start — the configured store does not implement the ABS session keyspace " +
			"(PebbleDB is the only supported backend)")
		os.Exit(1)
	}
	identityStore, ok := s.Store().(servermiddleware.ABSIdentityStore)
	if !ok {
		slog.Error("abs: refusing to start — the configured store cannot resolve ABS identities")
		os.Exit(1)
	}

	resolver := servermiddleware.NewABSIdentityResolver(cfg, verifier, oauthCfg, identityStore)
	handler, err := abshandler.New(abshandler.Options{
		Config:   cfg,
		Store:    absStore,
		Resolver: resolver,
		// UserData stays nil in Phase 1: no ABS progress records exist yet, so the
		// empty list genuinely IS the complete list. Phase 6 wires the real provider,
		// and the warning below makes the gap visible until it does.
		UserData: nil,
	})
	if err != nil {
		slog.Error("abs: refusing to start — handler construction failed", "err", err)
		os.Exit(1)
	}

	// Own group so the ABS surface carries its own body limit, distinct from /api/v1.
	// Rate limiting for /login and /auth/refresh is enforced inside the handler (per
	// source IP, counting FAILURES only) rather than as a blanket per-request limiter:
	// §1.9.4 item 3 — Absorb polls /ping every 20 s and syncs every 15 s, and a client
	// retry storm must not be able to lock out a real user or trip an edge WAF ban.
	absGroup := s.router.Group("")
	absGroup.Use(servermiddleware.MaxRequestBodySize(
		int64(config.AppConfig.JSONBodyLimitMB)*1024*1024,
		int64(config.AppConfig.UploadBodyLimitMB)*1024*1024,
	))
	handler.Register(absGroup)

	if !handler.HasUserDataProvider() {
		slog.Warn("abs: no media-progress provider is wired — /api/me will report an EMPTY mediaProgress list. " +
			"That is correct only while the server holds no ABS progress records at all: clients DELETE local " +
			"progress rows absent from this list. Phase 6 must wire the provider before any progress is stored.")
	}
	slog.Info("abs: Audiobookshelf-compatible auth surface enabled",
		"modes", strings.Join(cfg.ModeNames(), ","),
		"server_version", cfg.ServerVersion,
		"access_token_ttl", cfg.AccessTTL.String(),
		"refresh_token_ttl", cfg.RefreshTTL.String(),
		"refresh_grace", cfg.RefreshGrace.String(),
	)
}

// absRouteList is the registered ABS surface, for tests and for the startup log.
func absRouteList() []string {
	return []string{
		"GET /ping",
		"GET /status",
		"POST /login",
		"POST /auth/refresh",
		"POST /logout",
		"GET /api/me",
		"GET /api/me/sessions",
		"DELETE /api/me/sessions/:id",
	}
}
