// file: internal/server/handlers/abs/handler.go
// version: 1.4.0
// guid: fb0271c6-3a49-4d85-9e13-8c507b2ad64f
// last-edited: 2026-08-02

// Package abs implements the Audiobookshelf-compatible auth surface (design spec
// Phase 1): GET /ping, GET /status, POST /login, POST /auth/refresh, POST /logout,
// GET /api/me, GET /api/me/sessions and DELETE /api/me/sessions/:id.
//
// These routes are UNVERSIONED and live on their own top-level router group, not
// under /api/v1 (spec §3.6). Two consequences drive the design:
//
//   - Every route must be REGISTERED explicitly. The SPA is served from a NoRoute
//     catch-all, so an ABS path we forget answers 200 with index.html — and a 200
//     carrying HTML is fatal to both target clients' decoders (§1.8.6).
//   - The group has its own fail-closed identity middleware. It does NOT inherit the
//     fail-open Cloudflare-Access behaviour used on /api/v1, where an unverifiable
//     assertion falls through to a second gate. Here there is no second gate.
//
// The existing auth paths — `abk_` API keys, browser `sess:` sessions, OAuth, and the
// /api/v1 Cloudflare-Access middleware — are untouched. This package only adds.
package abs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
)

// Store is the narrow slice of the database the ABS auth surface needs.
//
// Declared here rather than added to database.Store so no shared interface file has
// to change; *database.PebbleStore satisfies it via pebble_store_abssession.go.
type Store interface {
	GetUserByID(id string) (*database.User, error)
	GetUserByUsername(username string) (*database.User, error)
	UpdateUser(user *database.User) error

	CreateABSSession(session *database.ABSSession) error
	GetABSSession(id string) (*database.ABSSession, error)
	GetABSSessionByRefreshHash(hash string) (*database.ABSSession, error)
	UpdateABSSession(session *database.ABSSession) error
	ListABSSessionsForUser(userID string) ([]database.ABSSession, error)
	RevokeABSSession(id string) error
	RevokeAllABSSessionsForUser(userID string) (int, error)
}

// UserDataProvider supplies the user-scoped payload that Phase 6 owns.
//
// MediaProgress MUST return the user's COMPLETE progress list. §1.8.1: AudioBooth's
// syncFromAPI DELETES every local progress row whose bookID is absent from the
// server's list, sparing only the currently-playing book. Returning a truncated or
// paginated list therefore destroys the user's listening positions on every
// home-screen refresh. An implementation that cannot produce the complete list must
// return an error, never a partial slice — this handler turns an error into a 5xx
// rather than a lying 200.
type UserDataProvider interface {
	MediaProgress(userID string) ([]any, error)
	Bookmarks(userID string) ([]any, error)

	// MediaProgressFor renders the single (user, book) row that GET
	// /api/me/progress/:id serves, reporting ok=false when the user has never
	// started the book (real ABS answers 404 there).
	//
	// It exists so the single-item body is produced by the SAME renderer as the
	// list rather than by a parallel one. Clients compare the two for the same
	// book — AudioBooth resolves conflicts on `lastUpdate` with strict `>` after
	// truncating to whole seconds — so two renderers that drift by a field or a
	// rounding step turn into a book that will not stop re-syncing.
	MediaProgressFor(userID, bookID string) (any, bool, error)

	// ListenedSeconds is the user's total listened time across every book they
	// have touched. It backs GET /api/me/listening-stats.
	//
	// Enumeration lives here rather than in the handler because this provider
	// already owns the ONE user-keyed prefix scan that makes a complete per-user
	// answer affordable on a request path (see userdata.go). A handler-side
	// re-implementation would either duplicate that scan or reach for a
	// whole-library one.
	ListenedSeconds(userID string) (float64, error)
}

// BookmarkStore is the named-bookmark CRUD slice (pebble_store_bookmarks.go).
//
// itemID here is the CLIENT-VISIBLE libraryItemId (the 36-char sync UUID), not a
// Book ULID: the keyspace was defined that way to mirror real ABS, whose
// create/update/delete surface addresses a bookmark by (item id, time) with the time
// value in the URL path. userdata.go's Bookmarks reader depends on the same choice.
type BookmarkStore interface {
	CreateBookmark(b progress.Bookmark) error
	ListBookmarks(userID, itemID string) ([]progress.Bookmark, error)
	UpdateBookmarkTitle(userID, itemID string, timeSec float64, newTitle string) error
	DeleteBookmark(userID, itemID string, timeSec float64) error
}

// LibraryStore is the read slice of the library the browse + playback surface needs
// (Phase 3 / Phase 5b). Every method already exists on database.Store; declaring the
// narrow interface here keeps store.go untouched and keeps the handler testable
// against a fake rather than a whole PebbleStore.
//
// Note what is NOT here: no writer of any kind. The ABS surface is read + play +
// progress only; management stays on /api/v1 (spec §3.6 router split), and the type
// system is what enforces that rather than a convention nobody rechecks.
type LibraryStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBooksByIDs(ids []string) ([]database.Book, error)
	GetAllBookSummaries(limit, offset int) ([]database.BookSummary, error)
	// The FILTERED pair is what the ABS item list uses. The unfiltered ones above
	// remain for callers that genuinely want every row; this surface does not, and
	// using them here is what showed 44,888 items instead of ~16,000.
	GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error)
	CountBookSummariesFiltered(f database.BookSummaryFilter) (int, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	CountAllBooks() (int, error)
	SearchBooks(query string, limit, offset int) ([]database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetAuthorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]database.Author, error)
	GetNarratorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]database.Narrator, error)
	GetSeriesByIDs(ids []int) (map[int]*database.Series, error)
	GetAllAuthors() ([]database.Author, error)
	GetAllAuthorBookCounts() (map[int]int, error)
	GetAllSeries() ([]database.Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
	ListNarrators() ([]database.Narrator, error)
	GetDistinctGenres() ([]string, error)
	GetDistinctLanguages() ([]string, error)
}

// IdentityStore is the sync_item + sync_file keyspace slice.
//
// 🔴 EVERY client-visible id on this surface comes from here and NOWHERE else.
// libraryItemId must be the 36-char sync_item UUID because Absorb splits compound
// ids by FIXED BYTE OFFSET substring(0,36) at 4+ call sites (§1.7.1) — our 26-char
// Book ULIDs would mis-truncate into the wrong /api/me/progress path. `ino` must be
// the sync_file id and never a real filesystem inode, because this app moves and
// reorganizes files as its core function and an inode does not survive a move across
// filesystems or a copy-then-replace (§4.2b) — that would break every offline
// client's cached download URL.
type IdentityStore interface {
	MintOrGetSyncID(bookID string) (string, error)
	ResolveSyncItem(syncID string) (*database.SyncItem, error)
	MintOrGetSyncFileID(bookID, fileID string) (string, error)
	GetSyncFileID(bookID, fileID string) (string, bool, error)
	ListSyncFilesForBook(bookID string) ([]database.SyncFile, error)
}

// ChapterStore reads the persisted per-book chapter timeline (Phase 4). Optional:
// with no chapter store the mapper synthesizes one chapter per track, which is what
// real ABS does for a multi-file book anyway.
type ChapterStore interface {
	GetChaptersForBook(bookID string) ([]database.Chapter, error)
}

// ProgressStore is the existing per-user listening-progress subsystem (spec §1: we
// adapt it, we do not rebuild it).
//
// The play + session-sync paths need it for one requirement in particular:
// PlaySession.currentTime must be the user's TRUE latest position. AudioBooth takes
// max() on position at session start while IGNORING timestamps
// (SessionManager.swift:175-180), so a 0 or a session-start snapshot here silently
// rewinds the user (§1.8.7). Optional — with no store, sessions still play, they
// just start at 0 and remember nothing.
type ProgressStore interface {
	GetUserPosition(userID, bookID string) (*database.UserPosition, error)
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	SetUserBookState(state *database.UserBookState) error
	// ClearUserPositions backs DELETE /api/me/progress/:id ("reset progress"). It
	// clears EVERY segment row for the (user, book), not just the ABS whole-book
	// one: the user asked to start the book over, and leaving the app's own
	// per-chapter positions behind would resurrect the old place the next time the
	// in-app reader touched the book.
	ClearUserPositions(userID, bookID string) error
}

// Options are the handler's dependencies.
type Options struct {
	Config   *absauth.Config
	Store    Store
	Resolver *servermiddleware.ABSIdentityResolver
	// UserData is optional in Phase 1 (no ABS progress records exist yet) and is
	// wired by Phase 6. When nil the handler serves empty arrays and logs a warning
	// once at construction, because an empty list is only safe while it is genuinely
	// the complete list.
	UserData UserDataProvider

	// Library and Identity together gate the browse + playback surface: with either
	// nil, Register skips those routes entirely rather than registering handlers that
	// would answer with a half-built body. wireABSRoutes refuses to boot when the
	// configured store cannot supply them, so a real server can never degrade
	// silently into the auth-only surface.
	Library  LibraryStore
	Identity IdentityStore
	Chapters ChapterStore
	Progress ProgressStore
	// Bookmarks gates the bookmark CRUD routes. Nil means they are not registered
	// at all rather than registered and answering 500 — a client that gets a 404
	// hides the feature, while one that gets a 500 shows an error on every open.
	Bookmarks BookmarkStore

	// CoverRoot is config.AppConfig.RootDir — the library root that
	// metadata.CoverPathForBook resolves covers under, and the base for the relative
	// paths reported on library items.
	CoverRoot string
	// LibraryName is the display name of the single library we expose. Empty means
	// "Books".
	LibraryName string
}

// itemsCountEntry is one cached filtered count and when it was computed.
type itemsCountEntry struct {
	count int
	at    time.Time
}

// Handler serves the ABS auth surface.
type Handler struct {
	cfg      *absauth.Config
	store    Store
	resolver *servermiddleware.ABSIdentityResolver
	userData UserDataProvider
	throttle *absauth.Throttle

	library     LibraryStore
	identity    IdentityStore
	chapters    ChapterStore
	progress    ProgressStore
	bookmarks   BookmarkStore
	coverRoot   string
	libraryName string

	// sessions holds the live play sessions. See play.go: they are in-memory on
	// purpose, and a sync for an id we do not know is answered idempotently rather
	// than 404'd, because AudioBooth cannot detect an expired session (it rewraps
	// errors and loses the status code) and so will never re-create one (§1.8.8 #8).
	sessions *sessionRegistry

	// refreshLocks holds one mutex per session id — the per-session single-flight
	// lock of §3.4 step 1. It serializes concurrent refreshes of the SAME session so
	// two simultaneous requests cannot rotate twice and orphan each other, while
	// refreshes of different sessions stay fully parallel.
	//
	// It is only ever keyed by a session id that already resolved from a real refresh
	// token or a real CF identity, so its size is bounded by the number of genuine
	// devices — an attacker replaying random tokens cannot make it grow.
	refreshLocks sync.Map // sessionID -> *sync.Mutex

	// itemsCount caches the filtered library-item count per filter identity.
	// CountBookSummariesFiltered is a full-library scan, and this endpoint is polled
	// on every library page, so an uncached count made latency a flat ~2s regardless
	// of which page was requested. See countItems in browse.go.
	itemsCountMu sync.Mutex
	itemsCount   map[string]itemsCountEntry

	// now and newID are injectable for deterministic tests.
	now   func() time.Time
	newID func() string
}

// New validates dependencies and constructs the handler. It refuses to build when the
// ABS API is disabled, so a misconfigured server registers nothing rather than
// serving a half-wired surface.
func New(o Options) (*Handler, error) {
	if o.Config == nil {
		return nil, errors.New("abs: config is required")
	}
	if !o.Config.Enabled {
		return nil, errors.New("abs: refusing to build handler for a disabled ABS API")
	}
	if o.Store == nil {
		return nil, errors.New("abs: store is required")
	}
	// A nil resolver would leave the surface authenticating nothing while /login still
	// worked — closed, but confusingly broken. Refuse to build instead.
	if o.Resolver == nil {
		return nil, errors.New("abs: identity resolver is required")
	}
	name := strings.TrimSpace(o.LibraryName)
	if name == "" {
		name = "Books"
	}
	h := &Handler{
		cfg:         o.Config,
		store:       o.Store,
		resolver:    o.Resolver,
		userData:    o.UserData,
		throttle:    absauth.NewThrottle(),
		library:     o.Library,
		identity:    o.Identity,
		chapters:    o.Chapters,
		progress:    o.Progress,
		bookmarks:   o.Bookmarks,
		coverRoot:   o.CoverRoot,
		libraryName: name,
		itemsCount:  map[string]itemsCountEntry{},
		now:         time.Now,
		newID:       func() string { return ulid.Make().String() },
	}
	h.sessions = newSessionRegistry(h.nowFn)
	return h, nil
}

// nowFn indirects through the handler so SetClock also moves session expiry.
func (h *Handler) nowFn() time.Time { return h.now() }

// HasBrowseSurface reports whether the Phase 3 / Phase 5b routes were registered.
func (h *Handler) HasBrowseSurface() bool { return h.library != nil && h.identity != nil }

// SetSleep replaces the throttle's delay function. Tests inject a no-op.
func (h *Handler) SetSleep(fn func(time.Duration)) { h.throttle.SetSleep(fn) }

// SetClock replaces the handler clock. Tests use it to cross a grace boundary.
func (h *Handler) SetClock(fn func() time.Time) {
	if fn != nil {
		h.now = fn
		h.throttle.SetClock(fn)
	}
}

// HasUserDataProvider reports whether Phase 6's progress/bookmark provider is wired.
func (h *Handler) HasUserDataProvider() bool { return h.userData != nil }

// Register registers every ABS route on the given router.
//
// Each route is registered EXPLICITLY and individually. That is not stylistic: the
// SPA NoRoute catch-all would otherwise answer any unregistered ABS path with a 200
// and index.html, and §1.8.6 / §1.7.3 item 11 make a 200-with-HTML fatal to both
// clients (the status guard passes, then the JSON cast fails).
//
// /ping and /status are intentionally unauthenticated at the app layer: §1.7.3 item
// 14 — /ping gates Absorb's whole online/offline state machine, and AudioBooth probes
// /status before it has anything to authenticate with. They remain
// Cloudflare-Access-authenticated at the edge (§1.9.3), so this is not a public
// surface.
func (h *Handler) Register(r gin.IRouter) {
	r.GET("/ping", h.Ping)
	r.GET("/status", h.Status)

	r.POST("/login", h.Login)
	r.POST("/auth/refresh", h.Refresh)

	// Single sign-on. Both are unauthenticated at the app layer by necessity:
	// /auth/openid is opened in the client's web session and derives identity
	// from the Cloudflare Access assertion on the request, and the callback is
	// authenticated by the one-time PKCE code it carries. See openid.go.
	r.GET("/auth/openid", h.OpenIDAuthorize)
	r.GET("/auth/openid/callback", h.OpenIDCallback)

	auth := servermiddleware.ABSRequireAuth(h.resolver)
	r.POST("/logout", auth, h.Logout)
	// Re-validates an existing credential and re-reports the user. Carries the same
	// §1.8.1 complete-mediaProgress obligation as /api/me — see authorize.go. Must
	// also appear in absReservedPaths, or gin 301s it into the app API.
	r.POST("/api/authorize", auth, h.Authorize)
	r.GET("/api/me", auth, h.Me)
	r.GET("/api/me/sessions", auth, h.Sessions)
	r.DELETE("/api/me/sessions/:id", auth, h.DeleteSession)

	if !h.HasBrowseSurface() {
		return
	}

	// ── Phase 3: library browse ─────────────────────────────────────────────
	r.GET("/api/libraries", auth, h.Libraries)
	r.GET("/api/libraries/:libraryId", auth, h.Library)
	r.GET("/api/libraries/:libraryId/items", auth, h.LibraryItems)
	r.GET("/api/libraries/:libraryId/personalized", auth, h.Personalized)
	r.GET("/api/libraries/:libraryId/series", auth, h.LibrarySeries)
	r.GET("/api/libraries/:libraryId/collections", auth, h.EmptyPage)
	r.GET("/api/libraries/:libraryId/playlists", auth, h.EmptyPage)
	r.GET("/api/libraries/:libraryId/authors", auth, h.LibraryAuthors)
	r.GET("/api/libraries/:libraryId/narrators", auth, h.LibraryNarrators)
	r.GET("/api/libraries/:libraryId/filterdata", auth, h.LibraryFilterData)
	r.GET("/api/libraries/:libraryId/search", auth, h.LibrarySearch)
	// Podcast stub: a probing client gets a valid empty response, not an error
	// (§1.8.6 — the wrapper key is required, and `{}` throws).
	r.GET("/api/libraries/:libraryId/recent-episodes", auth, h.RecentEpisodes)

	r.GET("/api/items/:id", auth, h.Item)
	// The cover endpoint is deliberately OUTSIDE the auth middleware. §1.8.8 item 7 /
	// §1.9.5: AudioBooth's widget extension sends no headers at all — not even
	// ?token= — and its widget cover art has no other path. It stays
	// Cloudflare-Access-gated at the edge in Modes B/C, and the residual exposure
	// (cover images fetchable by anyone who knows a 36-char item UUID; no metadata,
	// no audio, no progress) is the owner-accepted, documented tradeoff.
	r.GET("/api/items/:id/cover", h.ItemCover)

	// ── Phase 5b: playback ──────────────────────────────────────────────────
	r.POST("/api/items/:id/play", auth, h.Play)
	r.GET("/api/items/:id/file/:ino", auth, h.ItemFile)
	r.GET("/api/items/:id/file/:ino/download", auth, h.ItemFileDownload)
	r.POST("/api/session/:id/sync", auth, h.SessionSync)
	r.POST("/api/session/:id/close", auth, h.SessionClose)

	// UNAUTHENTICATED by protocol requirement (§1.8.3). AudioBooth has no
	// contentUrl field at all (zero repo-wide hits) and streams exclusively from
	// this path; the session id is the capability. It is a freshly minted 36-char
	// UUID, unguessable and scoped to one book for one user, and it stops working
	// when the session expires.
	r.GET("/public/session/:id/track/:index", h.PublicSessionTrack)

	// ── Phase 6: progress mutation ──────────────────────────────────────────
	//
	// Registered after the browse block because every one of them resolves a
	// client-visible libraryItemId through h.identity.
	//
	// gin routes the static "batch" sibling alongside the ":id" wildcard at the
	// same depth correctly on v1.12.0 (verified: no panic, no mis-dispatch), so
	// batch is a real route rather than a branch inside the :id handler.
	r.GET("/api/me/progress", auth, h.MediaProgressList)
	r.GET("/api/me/progress/:id", auth, h.MediaProgressGet)
	r.PATCH("/api/me/progress/:id", auth, h.MediaProgressPatch)
	r.PATCH("/api/me/progress/batch/update", auth, h.MediaProgressBatchUpdate)
	r.DELETE("/api/me/progress/:id", auth, h.MediaProgressDelete)
	// ── remove from Continue Listening ──────────────────────────────────────
	//
	// 🔴 THE PATH AND THE METHOD ARE BOTH NON-OBVIOUS. Read before "tidying".
	//
	// Verified against AudioBooth's own source (SessionService.swift:181-193,
	// MPL-2.0), which is the authority here — the oracle has no such route at all
	// and this spec's §1.8.6 recorded the wrong shape:
	//
	//	NetworkRequest(path: "/api/me/progress/\(progressID)/remove-from-continue-listening",
	//	               method: .get)
	//
	// So it is a **GET**, not a POST, and it hangs off **/api/me/progress/:id**,
	// not /api/me/item/:id. We shipped only the POST-on-/api/me/item form, so every
	// tap 404'd before reaching a handler — which is exactly why the owner reported
	// this as still broken after the Phase 6 write half shipped.
	//
	// :id is the mediaProgress ROW id; resolveBookID accepts that and the bare
	// libraryItemId. The response must be a NON-EMPTY JSON object: the client
	// decodes into an empty `struct Response: Codable {}` and NetworkService treats
	// an empty body as a decoding error (§1.8.6).
	r.GET("/api/me/progress/:id/remove-from-continue-listening", auth, h.RemoveFromContinueListening)
	// Tolerated aliases. Cheap, and each is a shape some ABS client or a future
	// AudioBooth version could plausibly use; a 404 here is user-visible breakage
	// while an extra route costs nothing.
	r.POST("/api/me/progress/:id/remove-from-continue-listening", auth, h.RemoveFromContinueListening)
	r.POST("/api/me/item/:id/remove-from-continue-listening", auth, h.RemoveFromContinueListening)
	r.GET("/api/me/item/:id/remove-from-continue-listening", auth, h.RemoveFromContinueListening)

	// ── Phase 6: listening statistics ───────────────────────────────────────
	//
	// 200 rather than 404 — NOT cosmetic. AudioBooth's NetworkService flips the
	// server's connection indicator to `.connectionError` (the orange dot) on ANY
	// non-2xx, and /api/me/listening-stats is fetched on every home-screen refresh.
	// See stats.go for why §1.8.6's "prefer 404" guidance was wrong.
	r.GET("/api/me/listening-stats", auth, h.ListeningStats)
	r.GET("/api/me/listening-sessions", auth, h.ListeningSessions)
	r.GET("/api/me/stats/year/:year", auth, h.YearStats)
	// Registered BEFORE the bookmark block so it is not gated on a bookmark store,
	// and note the literal "listening-sessions" sits where /api/me/item/:id would —
	// gin routes the static sibling ahead of the wildcard, which the tests pin.
	r.GET("/api/me/item/listening-sessions/:id", auth, h.ItemListeningSessions)

	if h.bookmarks == nil {
		return
	}
	// ── Phase 6: bookmarks CRUD ─────────────────────────────────────────────
	r.GET("/api/me/bookmarks/:id", auth, h.ListItemBookmarks)
	r.POST("/api/me/item/:id/bookmark", auth, h.CreateBookmark)
	r.PATCH("/api/me/item/:id/bookmark", auth, h.UpdateBookmark)
	// The TIME VALUE is the path parameter — real ABS keys a bookmark by
	// (libraryItemId, time), not by an opaque bookmark id.
	r.DELETE("/api/me/item/:id/bookmark/:time", auth, h.DeleteBookmark)
}

// HasBookmarkSurface reports whether the Phase 6 bookmark CRUD routes were
// registered.
func (h *Handler) HasBookmarkSurface() bool { return h.bookmarks != nil }

// ── shared helpers ──────────────────────────────────────────────────────────

// lockForSession returns the per-session single-flight mutex, creating it on demand.
func (h *Handler) lockForSession(sessionID string) *sync.Mutex {
	v, _ := h.refreshLocks.LoadOrStore(sessionID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// isActiveUser treats an empty status as active, matching RequireAuth's behaviour on
// /api/v1 for rows written before the column existed.
func isActiveUser(u *database.User) bool {
	if u == nil {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(u.Status))
	return s == "" || s == "active"
}

// userPayload loads the user's complete progress and bookmark lists.
//
// A provider error is propagated so the caller can answer 5xx. That is deliberate:
// serving a 200 with an empty mediaProgress when we simply could not read it would
// make AudioBooth delete every local progress row (§1.8.1). A 5xx just makes the
// client retry.
func (h *Handler) userPayload(userID string) (progress, bookmarks []any, err error) {
	if h.userData == nil {
		// Phase 1 has no ABS progress records at all, so the empty list IS the
		// complete list. Phase 6 wires a real provider.
		return []any{}, []any{}, nil
	}
	progress, err = h.userData.MediaProgress(userID)
	if err != nil {
		return nil, nil, err
	}
	bookmarks, err = h.userData.Bookmarks(userID)
	if err != nil {
		return nil, nil, err
	}
	if progress == nil {
		progress = []any{}
	}
	if bookmarks == nil {
		bookmarks = []any{}
	}
	return progress, bookmarks, nil
}

// buildAuthResponse renders the shared /login and /auth/refresh body.
func (h *Handler) buildAuthResponse(user *database.User, accessToken, refreshToken string, progress, bookmarks []any) authResponse {
	return authResponse{
		Source:         "audiobook-organizer",
		EreaderDevices: []ereaderDevice{},
		ServerSettings: h.buildServerSettings(),
		User:           h.buildUser(user, accessToken, refreshToken, progress, bookmarks),
		// §1.8.2 LOGIN BLOCKER: never null. Load() guarantees a 36-char UUID.
		UserDefaultLibraryID: h.cfg.DefaultLibraryID,
	}
}

// respondJSON writes a JSON body. It exists so no ABS handler can accidentally return
// an empty 200, which is fatal for any typed endpoint (§1.8.6).
func respondJSON(c *gin.Context, status int, body any) {
	c.JSON(status, body)
}

// respondError writes a JSON error body. Neither target client parses a non-200 body,
// but the body is still JSON (never HTML, never empty) as cheap hygiene for other
// clients and for our own logs.
func respondError(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": message})
}
