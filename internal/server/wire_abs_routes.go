// file: internal/server/wire_abs_routes.go
// version: 1.20.0
// guid: 9c6b13f8-40a2-4e57-b18d-72e0a5c4d396
// last-edited: 2026-08-28

package server

import (
	"context"
	"log/slog"
	"net/http"
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
	// NOTE: /api/collections is deliberately NOT here. It has a live /api/v1 twin, so
	// it belongs in absAppAPICollisions + absCollisionDetailRoutes, which are
	// method-aware and gated on ABSAPIEnabled. See the comment on that list.
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
	// NOTE: "/api/collections/" was here for exactly one commit, 2026-08-16, and the
	// comment justifying its width said "there is no /api/v1/collections twin — grep
	// confirms this namespace has exactly one implementation". That was TRUE WHEN
	// WRITTEN and FALSE ONE COMMIT LATER, when the native API in
	// handlers/collections.go created the twin — retroactively converting this into
	// the playlists defect, in the same PR that cited the playlists defect.
	//
	// The lesson generalises: "no /api/v1 twin exists" is a fact about a MOMENT, not
	// a property of a namespace, so it cannot justify a permanently wider
	// reservation. The method-aware, flag-gated list is correct whether or not a twin
	// exists today, which is why collections now lives there instead.
}

// absCollisionDetailRoutes are routes inside an absAppAPICollisions namespace that
// ABS serves natively — but ONLY when the ABS surface is actually enabled.
//
// 🔴 THIS LIST IS SEPARATE FROM THE ONE ABOVE FOR ONE REASON: the redirect
// middleware is NOT gated on ABSAPIEnabled. Putting "/api/playlists/" in
// absReservedPathPrefixes reserves it unconditionally, so on an ABS-DISABLED
// deployment — which is the default — GET /api/playlists/123 stops redirecting and
// starts 404ing, because the ABS route that would answer it was never registered.
// That is a working app route broken to make an unimplemented ABS one honest: the
// exact defect that took out 46 live app routes twice (#2332 → #2333 → #2335).
// TestCollidingNamespacesStillRedirect caught it here on the first run.
//
// Gating on the flag means: ABS off → redirect, byte-for-byte as before. ABS on →
// ABS serves it, which is what an ABS deployment asked for.
//
// THE TRAILING SLASH IS LOAD-BEARING. "/api/playlists" itself is NOT reserved even
// with ABS on, so the bare LIST keeps redirecting to the app-API twin; only
// "/api/playlists/<id>" is claimed.
//
// Why the detail path needed claiming at all: opening a playlist in the app calls
// GET /api/playlists/:id, which 301'd into /api/v1/playlists/:id and answered
// {"book_ids":[...]} instead of ABS's {"items":[{"libraryItem":…}]}. The client
// cannot parse that, so every playlist opened EMPTY — reported from the app
// 2026-08-13. The web UI is unaffected either way: it hardcodes an /api/v1 base
// (e.g. DelugeSettingsTab.tsx:24) and never uses the unversioned form.
// 🔴 THIS IS A ROUTE LIST, NOT A PREFIX LIST, AND THE DIFFERENCE WAS A LIVE BUG.
//
// It was written as a prefix subtree match — HasPrefix(path, "/api/playlists/") —
// which reserved the WHOLE subtree for ABS while ABS answers exactly one route in
// it, GET /api/playlists/:id. Measured against production 2026-08-13 immediately
// after that shipped, six working app routes had started 404ing instead of
// redirecting:
//
//	PUT    /api/playlists/:id              DELETE /api/playlists/:id
//	POST   /api/playlists/:id/books        DELETE /api/playlists/:id/books/:bookID
//	POST   /api/playlists/:id/reorder      POST   /api/playlists/:id/materialize
//
// That is the same defect as #2332 → #2333 → #2335 (46 app routes 404'd), recurring
// one level down, inside the very change whose comment warns about it. Reserving a
// namespace for ABS must be as narrow as what ABS actually serves: a reservation
// wider than the implementation converts working routes into 404s silently, because
// a 404 on a redirect is indistinguishable from a route that never existed.
//
// So the match is on METHOD plus the EXACT route shape. A deeper path or a different
// verb belongs to the app API and keeps redirecting.
//
// Originally this was method + "exactly one remaining segment", which was all
// playlists needed. Collections needs two-segment routes
// (/api/collections/:id/book/:bookId), so the rule is now a literal gin-style pattern
// match — which yields the one-segment behaviour as a special case rather than
// loosening it. Widening the matcher is safe in a way that widening a PREFIX is not:
// every route still has to be named explicitly, one line each.
type absCollisionDetailRoute struct {
	Method  string
	Pattern string // gin-style; ":name" matches exactly one non-empty segment
}

var absCollisionDetailRoutes = []absCollisionDetailRoute{
	{Method: http.MethodGet, Pattern: "/api/playlists/:id"},
	{Method: http.MethodGet, Pattern: "/api/authors/:id"},
	{Method: http.MethodGet, Pattern: "/api/series/:id"},

	// Collections, added 2026-08-16 with the ABS surface in handlers/abs/collections.go.
	// This list is EXACTLY the set registered there — no more (a wider claim 404s the
	// native twin) and no less (a narrower one 301s an ABS client into the app-API
	// shape). The routes the NATIVE api owns alone are deliberately absent and must
	// stay absent: GET /api/collections (ABS lists per-library), PUT
	// /api/collections/:id (ABS uses PATCH), POST /api/collections/:id/materialize.
	//
	// Caveat on POST /api/collections with the ABS surface OFF: it redirects, and a
	// 301 drops the body on many clients, so a create can arrive empty. That is the
	// pre-existing behaviour of the compat redirect for every app-API POST, not
	// something specific to collections, and the alternative — reserving it
	// unconditionally — is strictly worse, because it 404s a route the native twin
	// answers. Fixing it properly means 308 for non-GET across the whole redirect.
	{Method: http.MethodPost, Pattern: "/api/collections"},
	{Method: http.MethodGet, Pattern: "/api/collections/:id"},
	{Method: http.MethodPatch, Pattern: "/api/collections/:id"},
	{Method: http.MethodDelete, Pattern: "/api/collections/:id"},
	{Method: http.MethodPost, Pattern: "/api/collections/:id/book"},
	{Method: http.MethodDelete, Pattern: "/api/collections/:id/book/:bookId"},
}

// absCollisionDetailReserved reports whether (method, path) is a route ABS serves
// natively inside a colliding namespace. The caller must AND this with ABSAPIEnabled.
func absCollisionDetailReserved(method, path string) bool {
	for _, r := range absCollisionDetailRoutes {
		if method == r.Method && absPatternMatches(r.Pattern, path) {
			return true
		}
	}
	return false
}

// absPatternMatches reports whether path matches a gin-style route pattern, where a
// ":name" segment matches exactly one non-empty path segment.
//
// Segment-COUNT equality is the load-bearing part: it is what stops
// "/api/collections/:id" from claiming "/api/collections/abc/materialize", which is a
// native-only route. A HasPrefix match would claim it, and that is precisely the bug
// this whole mechanism exists to prevent.
func absPatternMatches(pattern, path string) bool {
	ps := strings.Split(pattern, "/")
	xs := strings.Split(path, "/")
	if len(ps) != len(xs) {
		return false
	}
	for i, seg := range ps {
		if strings.HasPrefix(seg, ":") {
			if xs[i] == "" {
				return false
			}
			continue
		}
		if seg != xs[i] {
			return false
		}
	}
	return true
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
// Matched as exact path OR subtree, so /api/collections and /api/collections/:id both 404.
//
// # Why this list is SHORTER than the set of ABS namespaces we lack
//
// A namespace only belongs here if the redirect leads nowhere. Three ABS namespaces —
// authors, series, playlists — collide with namespaces the APP API really serves under
// /api/v1 (19, 18 and 9 routes respectively, see wire_entities_routes.go and
// wire_dedup_routes.go). For those, the 301 is not a lie: it lands on a working handler.
// Listing them here 404s a live route to make an unimplemented one honest, which is a
// strictly worse trade — and because this middleware is NOT gated on ABSAPIEnabled
// (server_lifecycle.go:1219), it would do that on every deployment, including ones with
// the ABS surface switched off.
//
// That is not hypothetical: they WERE listed here when this shipped in #2332, and the
// original justification — "nothing in-repo requests these without the /v1 prefix" —
// checked the CALLER side of the boundary and never the TARGET side. The compatibility
// shim exists precisely for callers that are not in this repo.
//
// So the rule for adding a namespace here is: no /api/v1 twin → honest 404 belongs
// here; a twin exists → leave it out and let the redirect work, which
// TestCollidingNamespacesStillRedirect pins.
//
// # Do NOT check for a twin with grep — it cannot see one
//
// The #2333 fix said "grep for app-API routes under the same name first", and that
// advice was itself wrong: it missed /api/users, which has SEVEN live app routes
// (wire_library_routes.go:88). gin composes a route's path from its RouterGroup at
// registration time, so a grouped route's final path — `users := protected.Group("/users")`
// then `users.GET("", ...)` — exists as a literal NOWHERE in the source. Any text search
// for a route path is structurally blind to grouped registrations, and this codebase
// registers both ways: six prefixed groups today (/auth, /bench, /deluge, /itunes,
// /plugins, /users), everything else direct. Six is enough — /users was one of them.
//
// The only complete oracle is the flattened router: s.router.Routes(). That is what
// TestUnimplementedNamespacesHaveNoAppAPITwin walks, so the check no longer depends on
// anyone remembering to run the right grep.
//
// If one of these is ever implemented on the ABS surface, MOVE it to the lists above
// rather than deleting it — it must stay excluded from the redirect either way.
var absUnimplementedNamespaces = []string{
	// "/api/collections" was here until 2026-08-16, when the surface was actually
	// implemented (handlers/abs/collections.go). It moved to absReservedPaths +
	// absReservedPathPrefixes below. Leaving it here would have been harmless to
	// ROUTING — absReservedPath() matches either list, so the redirect is skipped
	// either way — but it would have left a list titled "endpoints we do NOT
	// implement" naming one we do, and TestUnimplementedABSNamespacesAre404NotRedirect
	// derives its assertions from this list, so it would have demanded a 404 from
	// the route that now answers.
	"/api/podcasts",
}

// absAppAPICollisions are ABS namespaces we do NOT implement but must NOT 404, because
// the app API serves the same name under /api/v1 and the compatibility redirect is the
// only way an unversioned caller reaches it. Declared as data rather than left implicit
// so the regression test can iterate it, and so the next person to extend the list above
// sees the exclusion instead of rediscovering it.
var absAppAPICollisions = []string{
	"/api/authors",
	"/api/series",
	"/api/playlists",
	// Added 2026-08-12, a second instance of the same defect: #2332 404'd this too and
	// #2333 did not catch it, because its twin is registered through a RouterGroup
	// (`protected.Group("/users")`, wire_library_routes.go:88) and the grep used only
	// matched direct registrations. Seven routes, including reset-password — an
	// account-recovery path.
	"/api/users",
	// Added 2026-08-16. Unlike its neighbours here, collections IS implemented on the
	// ABS surface — but only partly, and the partial overlap is the whole point: ABS
	// serves six routes in this namespace and the app API serves six DIFFERENT ones,
	// with only three shared. The namespace as a whole therefore belongs to the app
	// API (redirect by default) and ABS claims its six routes individually, by method
	// and shape, in absCollisionDetailRoutes.
	"/api/collections",
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
	slog.Info("abs: Audiobookshelf-compatible surface", "enabled", snap.ABSAPIEnabled)
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

	absStore, ok := s.Ops().(abshandler.Store)
	if !ok {
		slog.Error("abs: refusing to start — the configured store does not implement the ABS session keyspace " +
			"(PebbleDB is the only supported backend)")
		os.Exit(1)
	}
	identityStore, ok := s.Ops().(servermiddleware.ABSIdentityStore)
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
	libraryStore, ok := s.Ops().(abshandler.LibraryStore)
	if !ok {
		slog.Error("abs: refusing to start — the configured store cannot serve the library browse surface " +
			"(PebbleDB is the only supported backend)")
		os.Exit(1)
	}
	syncIdentity := database.AsSyncIdentityStore(s.Ops())
	syncFiles := database.AsSyncFileStore(s.Ops())
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
	progressList, ok := s.Ops().(abshandler.ProgressListStore)
	if !ok {
		slog.Error("abs: refusing to start — the configured store cannot enumerate a user's listening positions, " +
			"so /api/me could only ever report an EMPTY mediaProgress list. Clients DELETE local progress rows " +
			"absent from that list, so serving it would destroy the owner's place in every book.")
		os.Exit(1)
	}
	bookmarkStore := database.AsBookmarkStore(s.Ops())
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
		Chapters:    asChapterStore(s.Ops()),
		Playlists:   asPlaylistStore(s.Ops()),
		Collections: asCollectionStore(s.Ops()),
		Progress:    asProgressStore(s.Ops()),
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
	//
	// The store is passed as an ARGUMENT, so it is read here, synchronously, and
	// cannot be read inside the goroutine. See spawnContributorWarm for why that
	// is a parameter rather than a closure capture.
	spawnContributorWarm(s.Ops(), handler.WarmContributors)

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
	if config.AppConfig.ABSAuthProbeEnabled {
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
	if asProgressStore(s.Ops()) == nil {
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
		// OpenID web-flow endpoints (Handler.Register :380-381). Absent from this
		// list from their introduction until 2026-08-14, which made the list's
		// "every registered route" claim false — the N-8 audit finding, confirmed
		// against a runtime router.Routes() dump (47 listed vs 49 real; see
		// docs/reference/abs-implementation-status.md). Root paths, so the /api
		// reserved-path guard ignores them by design.
		"GET /auth/openid",
		"GET /auth/openid/callback",
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
		"GET /api/libraries/:libraryId/playlists", // real data since 2026-08-13; was h.EmptyPage
		// Detail route. The list shipped without it, so opening a playlist 301'd
		// into the app API and rendered empty — reported from the app 2026-08-13.
		"GET /api/playlists/:id",
		"GET /api/libraries/:libraryId/authors",
		"GET /api/authors/:id",
		"GET /api/series/:id",
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
		// Offline upload, single-session half (§1.8.8 item 1). A 2xx stub: it
		// exists so ShelfPlayer's maxAttempts:1 probe cannot 404 the connection
		// offline. Covered by the "/api/session/" entry in
		// absReservedPathPrefixes — listed HERE because
		// TestABSReservedPath_CoversEVERYRegisteredUnversionedRoute walks this
		// list, so a route missing from it is a route the guard never checks.
		"POST /api/session/local",
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

// asPlaylistStore returns the user-playlist capability, or nil when the store does
// not have it. Optional: a nil store keeps GET /api/libraries/:id/playlists
// answering the empty page it answered before this was wired, which is a valid
// Page<T> — never a 500.
func asPlaylistStore(s any) abshandler.PlaylistStore {
	if ps, ok := s.(abshandler.PlaylistStore); ok {
		return ps
	}
	return nil
}

// asCollectionStore narrows the store to the collection slice, or nil when the
// backend does not implement it. Nil is a supported state: the list route falls
// back to the empty page and the write routes report the feature unavailable,
// rather than the whole ABS surface failing to build.
func asCollectionStore(s any) abshandler.CollectionStore {
	if cs, ok := s.(abshandler.CollectionStore); ok {
		return cs
	}
	return nil
}

// spawnContributorWarm waits for the memdb warmup, then builds the contributor
// cache, on a background goroutine.
//
// The store is a PARAMETER, and that is the entire reason this function exists.
//
// s.store is a plain field that Server.Start later overwrites with the Bleve
// indexedStore wrapper (server_lifecycle.go). This goroutine is spawned at
// route-wiring time, from NewServer, BEFORE Start runs. So reading s.Ops()
// from the goroutine body is an unsynchronized read racing that write, and the
// two outcomes used to disagree: the old bare store.(*database.PebbleStore)
// assertion succeeded against the bare store and failed against the wrapper, so
// whether the warm waited at all was decided by scheduling. Losing that race
// means building the cache against a half-published memdb and serving a view of
// a library that does not exist for the whole TTL.
//
// resolveWarmupWaiter alone would only make both outcomes CORRECT; it leaves the
// unsynchronized read in place. Hoisting the read out of the goroutine removes
// the race -- but a hoist inside the caller is held in place by nothing except a
// comment asking the next editor not to move one line. That is not a guarantee,
// and the failure it protects against is silent.
//
// Passing the store as an argument makes it a language guarantee instead: Go
// evaluates arguments in the CALLER, before the callee runs, so the goroutine
// has no expression that could reach s.store however this is later edited.
//
// This also makes the ordering testable, which it previously was not. The old
// shape could only be exercised through wireABSRoutes, which returns early
// unless ABSAPIEnabled is set and calls os.Exit(1) on a misconfigured ABS
// block -- so -race never entered this path at all, and the detector's silence
// on the racy version was absence of coverage, not absence of a race.
func spawnContributorWarm(store any, warm func(context.Context)) {
	go func() {
		if w, ok := resolveWarmupWaiter(store); ok {
			w.WaitForWarmup()
		}
		warm(context.Background())
	}()
}

// warmupWaiter is the one method the contributor-cache warm needs before it may
// read the memdb.
type warmupWaiter interface {
	WaitForWarmup()
}

// resolveWarmupWaiter finds the memdb warmup gate through the Bleve indexedStore
// decorator that Server.Start installs.
//
// This is a named function rather than an inline assertion for the same reason
// resolveVGBackfiller is: a guard that cannot reach the production call site does
// not guard it. TestWarmupWaiterResolvesThroughDecorator calls THIS, so reverting
// it to a bare `s.Ops().(*database.PebbleStore)` turns that test red.
//
// The bare form was live until 2026-08-19 and its failure mode is the one the
// comment at the call site describes: skip the wait, build the contributor cache
// against a half-published memdb, and serve that view of a library that does not
// exist for the whole TTL. Its argument is now captured synchronously at wire
// time, so the value reaching here is the bare store deterministically and this
// resolves trivially; going through the chain is what keeps that true if the
// wiring order ever changes and a decorated store starts arriving instead.
func resolveWarmupWaiter(s any) (warmupWaiter, bool) {
	return database.AsCapability[warmupWaiter](s)
}
