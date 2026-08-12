// file: internal/server/wire_abs_routes.go
// version: 1.6.0
// guid: 9c6b13f8-40a2-4e57-b18d-72e0a5c4d396
// last-edited: 2026-08-12

package server

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Compile-time proof that the production store satisfies the Phase 6 capability the
// block in wireABSRoutes asserts at runtime. Without this, a signature drift on
// either side would turn into an os.Exit(1) at boot instead of a build failure —
// and the failure mode it guards is the /api/me empty-list data loss of §1.8.1.
var _ abshandler.ProgressListStore = (*database.PebbleStore)(nil)

// Same proof for the Phase 6 write half. ProgressStore gained ClearUserPositions
// (DELETE /api/me/progress/:id) and BookmarkStore is asserted at runtime below; a
// signature drift on either would otherwise surface as an os.Exit(1) at boot — or,
// for ProgressStore, as a SILENTLY DISABLED write surface, since asProgressStore
// answers nil on a failed assertion rather than exiting.
var (
	_ abshandler.ProgressStore = (*database.PebbleStore)(nil)
	_ abshandler.BookmarkStore = (*database.PebbleStore)(nil)
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
	// POST. Omitting it let gin 301 the call into /api/v1/authorize; the client
	// downgraded the redirected POST to GET, took a 404, and never refreshed its
	// session — "connected", then 401 on the next call. Observed in prod 2026-08-01.
	"/api/authorize",
	"/api/me",
	"/api/libraries",
}

// absReservedPathPrefixes covers ABS sub-trees (e.g. /api/me/sessions/:id).
//
// Adding a route to Handler.Register WITHOUT adding it here is the single most
// dangerous mistake on this surface, because it fails in the most misleading possible
// way: the endpoint exists, the route log lists it, a curl against it appears to work
// (301 → 200), and the client silently receives the /api/v1 app-API shape or a 401.
// It looks implemented and behaves broken.
var absReservedPathPrefixes = []string{
	"/api/me/",
	"/api/libraries/",
	"/api/items/",
	"/api/session/",
}

// absUnimplementedNamespaces are ABS endpoints we do NOT implement, reserved for the
// opposite reason to the lists above: not to protect a route we serve, but to make sure
// the ones we DON'T serve fail honestly.
//
// Without these, an ABS client probing /api/collections gets a 301 into
// /api/v1/collections — the app API — and meets a different JSON shape or a 401. That is
// the exact "looks implemented, behaves broken" failure the comment above warns about,
// arrived at from the other direction. An honest 404 lets a client hide the feature;
// a 301 into a foreign shape makes it look present and broken, and any non-2xx flips
// AudioBooth's connection indicator.
//
// Matched as exact path OR subtree, so /api/authors and /api/authors/:id both 404.
//
// Safe because nothing in-repo requests these without the /v1 prefix — the SPA's only
// bare /api/ call is /api/events (SSE), which is deliberately absent from this list.
//
// If one of these is ever implemented on the ABS surface, MOVE it to the lists above
// rather than deleting it — it must stay excluded from the redirect either way.
var absUnimplementedNamespaces = []string{
	"/api/collections",
	"/api/playlists",
	"/api/authors",
	"/api/series",
	"/api/users",
	"/api/podcasts",
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
	for _, ns := range absUnimplementedNamespaces {
		if path == ns || strings.HasPrefix(path, ns+"/") {
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

	// Phase 3 / Phase 5b capabilities. Each is a type assertion rather than a Store
	// interface change (repo rule: store.go stays untouched, new capabilities live in
	// their own file and are obtained by assertion). PebbleDB is the only supported
	// backend and satisfies all of them.
	//
	// FAIL CLOSED on the two that gate the surface: without them the browse + playback
	// routes would not be registered at all, so /api/libraries would answer a JSON 404
	// while the startup log claimed the ABS API was up. Exiting is the honest outcome.
	libraryStore, ok := s.Store().(abshandler.LibraryStore)
	if !ok {
		slog.Error("abs: refusing to start — the configured store cannot serve the library browse surface " +
			"(PebbleDB is the only supported backend)")
		os.Exit(1)
	}
	syncIdentity := database.AsSyncIdentityStore(s.Store())
	syncFiles := database.AsSyncFileStore(s.Store())
	if syncIdentity == nil || syncFiles == nil {
		slog.Error("abs: refusing to start — the configured store lacks the sync_item/sync_file keyspaces. " +
			"Every client-visible id (libraryItemId, ino) must come from them: a raw Book ULID is 26 chars and " +
			"Absorb splits ids at a fixed offset of 36, and a filesystem inode does not survive a file move.")
		os.Exit(1)
	}

	// Phase 6: the user-scoped payload of /api/me, /login and /auth/refresh.
	//
	// FAIL CLOSED, and harder than the rest of this function does. Once the store
	// holds a single ABS progress record, a nil provider is not "not implemented yet"
	// — it is active data destruction: /api/me would answer a 200 with an empty
	// mediaProgress, and AudioBooth DELETES every local progress row absent from that
	// list (§1.8.1). Exiting is the only honest outcome, and it is unreachable in
	// practice because PebbleDB (the only supported backend) satisfies both
	// capabilities asserted here, as well as the sync-identity and library ones
	// already asserted above.
	progressList, ok := s.Store().(abshandler.ProgressListStore)
	if !ok {
		slog.Error("abs: refusing to start — the configured store cannot enumerate a user's listening positions, " +
			"so /api/me could only ever report an EMPTY mediaProgress list. Clients DELETE local progress rows " +
			"absent from that list, so serving it would destroy the owner's place in every book.")
		os.Exit(1)
	}
	bookmarkStore := database.AsBookmarkStore(s.Store())
	if bookmarkStore == nil {
		slog.Error("abs: refusing to start — the configured store lacks the named-bookmark keyspace, so /api/me " +
			"could only ever report an EMPTY bookmarks list.")
		os.Exit(1)
	}
	userData, err := abshandler.NewUserData(abshandler.UserDataOptions{
		Progress:  progressList,
		Bookmarks: bookmarkStore,
		// syncIdentity supplies libraryItemId. It MUST be the 36-char sync UUID: a raw
		// 26-char Book ULID mis-truncates at Absorb's fixed offset of 36.
		Identity: syncIdentity,
		// libraryStore is the duration source (sum-of-tracks, §5b). `isFinished:true`
		// with a zero duration sets the client's currentTime to 0.
		Library: libraryStore,
	})
	if err != nil {
		slog.Error("abs: refusing to start — the media-progress provider could not be built", "err", err)
		os.Exit(1)
	}

	resolver := servermiddleware.NewABSIdentityResolver(cfg, verifier, oauthCfg, identityStore)
	handler, err := abshandler.New(abshandler.Options{
		Config:   cfg,
		Store:    absStore,
		Resolver: resolver,
		// The COMPLETE per-user progress + bookmark lists. Never nil, never paginated:
		// see the fail-closed block above and internal/server/handlers/abs/userdata.go.
		UserData: userData,

		Library:  libraryStore,
		Identity: absIdentityAdapter{SyncIdentityStore: syncIdentity, SyncFileStore: syncFiles},
		// Chapters and Progress are OPTIONAL by design: without chapters the mapper
		// synthesizes one per track (what real ABS does for a multi-file book anyway),
		// and without progress a session still plays, it just starts at 0.
		Chapters: asChapterStore(s.Store()),
		Progress: asProgressStore(s.Store()),
		// Phase 6 write half. Already asserted non-nil above (bookmarkStore), so
		// the CRUD routes always register on the supported backend.
		Bookmarks:   bookmarkStore,
		CoverRoot:   config.AppConfig.RootDir,
		LibraryName: "Books",
	})
	if err != nil {
		slog.Error("abs: refusing to start — handler construction failed", "err", err)
		os.Exit(1)
	}

	// Warm the contributor cache in the background. Building it is a full-library
	// scan (~6s here), so without this the FIRST request after every restart pays
	// it — and that request is normally the client's Authors tab, which then looks
	// like a hang. Measured: 6,104ms cold vs 105ms warm.
	//
	// 🔑 Waits for the memdb warmup first. The cache stores the set of authors of
	// VISIBLE books; building it against a half-published memdb would cache a view
	// of a library that does not exist yet, and it would then be served for the
	// whole TTL. A slow-but-correct warm beats a fast wrong one.
	go func() {
		if ps, ok := s.Store().(*database.PebbleStore); ok {
			ps.WaitForWarmup()
		}
		handler.WarmContributors(context.Background())
	}()

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

	// Opt-in diagnostic: log which credentials each ABS client actually sends. Off
	// unless ABS_AUTH_PROBE is set, because these routes are polled every 15-20s and
	// an always-on per-request line is journal noise outside a diagnostic window.
	// Registered FIRST so it observes every request, including ones the identity
	// middleware then rejects — a request that 401s is exactly the one worth seeing.
	if strings.TrimSpace(os.Getenv(servermiddleware.ABSAuthProbeEnvVar)) != "" {
		absGroup.Use(servermiddleware.ABSAuthProbe())
		slog.Warn("abs: auth probe ENABLED — every ABS request logs which credentials it carried. " +
			"Credential VALUES are never logged (booleans and lengths only), but this is verbose; " +
			"unset " + servermiddleware.ABSAuthProbeEnvVar + " when the diagnostic window is over")
	}
	handler.Register(absGroup)

	if !handler.HasUserDataProvider() {
		slog.Error("abs: the media-progress provider was NOT wired — /api/me would report an EMPTY mediaProgress " +
			"list, and clients DELETE local progress rows absent from it. This should be unreachable: " +
			"NewUserData above exits on failure.")
		os.Exit(1)
	}
	if !handler.HasBookmarkSurface() {
		slog.Error("abs: the bookmark CRUD routes were NOT registered — creating or deleting a bookmark would 404. " +
			"This should be unreachable: the bookmark-keyspace assertion above exits on failure.")
		os.Exit(1)
	}
	if !handler.HasBrowseSurface() {
		slog.Error("abs: the browse + playback routes were NOT registered — /api/libraries, /api/items and " +
			"/public/session would answer 404. This should be unreachable: the store assertions above exit on failure.")
		os.Exit(1)
	}
	if asProgressStore(s.Store()) == nil {
		slog.Warn("abs: no listening-progress store is wired — every play session will report currentTime 0. " +
			"AudioBooth takes max() on position at session start while ignoring timestamps, so a 0 there silently " +
			"rewinds the listener to the start of the book.")
	}

	slog.Info("abs: Audiobookshelf-compatible surface enabled (auth + library browse + direct playback + user progress/bookmarks)",
		"modes", strings.Join(cfg.ModeNames(), ","),
		"server_version", cfg.ServerVersion,
		"access_token_ttl", cfg.AccessTTL.String(),
		"refresh_token_ttl", cfg.RefreshTTL.String(),
		"refresh_grace", cfg.RefreshGrace.String(),
		"library_id", cfg.DefaultLibraryID,
		"library_root", config.AppConfig.RootDir,
		"routes", len(absRouteList()),
	)
	// Logged individually so an operator can diff the live surface against the ABS
	// protocol without reading the source, and so a route that exists but was never
	// added to absReservedPath is visible next to the ones that were.
	for _, route := range absRouteList() {
		slog.Info("abs: route registered", "route", route)
	}
}

// absIdentityAdapter joins the two independent sync keyspaces into the single
// interface the handler consumes. They are separate store files on purpose (separate
// locks, separate concerns); the handler only cares that both are present.
type absIdentityAdapter struct {
	database.SyncIdentityStore
	database.SyncFileStore
}

// asChapterStore returns the persisted-chapter capability, or nil when the store does
// not have it. Optional: the mapper synthesizes chapters from track durations
// otherwise.
func asChapterStore(s any) abshandler.ChapterStore {
	if cs, ok := s.(abshandler.ChapterStore); ok {
		return cs
	}
	return nil
}

// asProgressStore returns the listening-progress capability, or nil. Optional, but a
// nil one means PlaySession.currentTime is always 0 — which silently rewinds the user
// (§1.8.7) — so the caller warns about it.
func asProgressStore(s any) abshandler.ProgressStore {
	if ps, ok := s.(abshandler.ProgressStore); ok {
		return ps
	}
	return nil
}

// absRouteList is the registered ABS surface, for tests and for the startup log.
//
// Every entry here must also be covered by absReservedPath above (unversioned /api
// paths only) or it 301s into /api/v1 and looks implemented while behaving broken.
func absRouteList() []string {
	return []string{
		"GET /ping",
		"GET /status",
		"POST /login",
		"POST /auth/refresh",
		"POST /logout",
		"POST /api/authorize",
		"GET /api/me",
		"GET /api/me/sessions",
		"DELETE /api/me/sessions/:id",
		// Phase 3 — library browse.
		"GET /api/libraries",
		"GET /api/libraries/:libraryId",
		"GET /api/libraries/:libraryId/items",
		"GET /api/libraries/:libraryId/personalized",
		"GET /api/libraries/:libraryId/series",
		"GET /api/libraries/:libraryId/collections",
		"GET /api/libraries/:libraryId/playlists",
		"GET /api/libraries/:libraryId/authors",
		"GET /api/libraries/:libraryId/narrators",
		"GET /api/libraries/:libraryId/filterdata",
		"GET /api/libraries/:libraryId/search",
		"GET /api/libraries/:libraryId/recent-episodes",
		"GET /api/items/:id",
		"GET /api/items/:id/cover (no credentials required)",
		// Phase 5b — playback, direct play only.
		"POST /api/items/:id/play",
		"GET /api/items/:id/file/:ino",
		"GET /api/items/:id/file/:ino/download",
		"POST /api/session/:id/sync",
		"POST /api/session/:id/close",
		"GET /public/session/:id/track/:index (unauthenticated)",
		// Phase 6 — progress mutation. Every one is covered by the "/api/me/"
		// entry in absReservedPathPrefixes; they are listed here because
		// TestABSReservedPath_CoversEVERYRegisteredUnversionedRoute walks THIS
		// list, so a route missing from it is a route the guard never checks.
		"GET /api/me/progress",
		"GET /api/me/progress/:id",
		"PATCH /api/me/progress/:id",
		"PATCH /api/me/progress/batch/update",
		"DELETE /api/me/progress/:id",
		// The shape AudioBooth actually sends (GET, under /api/me/progress) plus
		// tolerated aliases — see Handler.Register.
		"GET /api/me/progress/:id/remove-from-continue-listening",
		"POST /api/me/progress/:id/remove-from-continue-listening",
		"POST /api/me/item/:id/remove-from-continue-listening",
		"GET /api/me/item/:id/remove-from-continue-listening",
		// Phase 6 — listening statistics. 200, not 404: any non-2xx flips the
		// client's connection indicator orange (see stats.go).
		"GET /api/me/listening-stats",
		"GET /api/me/listening-sessions",
		"GET /api/me/stats/year/:year",
		"GET /api/me/item/listening-sessions/:id",
		// Phase 6 — bookmarks CRUD.
		"GET /api/me/bookmarks/:id",
		"POST /api/me/item/:id/bookmark",
		"PATCH /api/me/item/:id/bookmark",
		"DELETE /api/me/item/:id/bookmark/:time",
	}
}
