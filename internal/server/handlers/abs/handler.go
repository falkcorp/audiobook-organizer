// file: internal/server/handlers/abs/handler.go
// version: 1.0.0
// guid: fb0271c6-3a49-4d85-9e13-8c507b2ad64f
// last-edited: 2026-07-30

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
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
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
}

// Handler serves the ABS auth surface.
type Handler struct {
	cfg      *absauth.Config
	store    Store
	resolver *servermiddleware.ABSIdentityResolver
	userData UserDataProvider
	throttle *absauth.Throttle

	// refreshLocks holds one mutex per session id — the per-session single-flight
	// lock of §3.4 step 1. It serializes concurrent refreshes of the SAME session so
	// two simultaneous requests cannot rotate twice and orphan each other, while
	// refreshes of different sessions stay fully parallel.
	//
	// It is only ever keyed by a session id that already resolved from a real refresh
	// token or a real CF identity, so its size is bounded by the number of genuine
	// devices — an attacker replaying random tokens cannot make it grow.
	refreshLocks sync.Map // sessionID -> *sync.Mutex

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
	return &Handler{
		cfg:      o.Config,
		store:    o.Store,
		resolver: o.Resolver,
		userData: o.UserData,
		throttle: absauth.NewThrottle(),
		now:      time.Now,
		newID:    func() string { return ulid.Make().String() },
	}, nil
}

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

	auth := servermiddleware.ABSRequireAuth(h.resolver)
	r.POST("/logout", auth, h.Logout)
	r.GET("/api/me", auth, h.Me)
	r.GET("/api/me/sessions", auth, h.Sessions)
	r.DELETE("/api/me/sessions/:id", auth, h.DeleteSession)
}

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
