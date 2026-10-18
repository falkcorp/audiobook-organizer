// file: internal/server/wire_library_routes.go
// version: 1.11.0
// guid: b2c3d4e5-f6a7-8901-bcde-f23456789012
// last-edited: 2026-10-18

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"net/http"
)

// wireLibraryRoutes registers cache, activity, split-book, filesystem, organize,
// metadata-cache, reading, playlists, user-management, version-groups, and
// admin-only routes on the protected group.
func (s *Server) wireLibraryRoutes(
	protected *gin.RouterGroup,
	cacheH *handlers.CacheHandler,
	activityH *handlers.ActivityHandler,
	activityCompactH *handlers.ActivityCompactHandler,
	splitBookH *handlers.SplitBookHandler,
	filesystemH *handlers.FilesystemHandler,
	organizeH *handlers.OrganizeHandler,
	metaCacheH *handlers.MetadataCacheHandler,
	readingH *handlers.ReadingHandler,
	playlistH *handlers.PlaylistHandler,
	collectionH *handlers.CollectionHandler,
	userH *handlers.UserHandler,
	versionsH *handlers.VersionsHandler,
) {
	// Cache stats (operational metrics — auth-gated per SEC-7)
	protected.GET("/cache/stats", s.perm(auth.PermLibraryView), cacheH.HandleCacheStats)
	protected.GET("/cache/stats/history", s.perm(auth.PermLibraryView), cacheH.HandleCacheStatsHistory)

	// Activity log
	protected.GET("/activity", s.perm(auth.PermLibraryView), activityH.ListActivity)
	protected.GET("/activity/sources", s.perm(auth.PermLibraryView), activityH.ListActivitySources)
	protected.POST("/activity/compact", s.perm(auth.PermSettingsManage), activityCompactH.CompactActivity)
	// POST /activity/clamp-summaries (the retroactive summary clamp plus
	// vacuum) had no caller in the repo and no replacement (01-P81g). It
	// answers 410 Gone for one release (owner decision D9) and is then
	// removed. The settings.manage guard still runs first, so an
	// unauthenticated caller gets 401, a caller without the permission 403,
	// and only an allowed caller learns it is gone.
	protected.POST("/activity/clamp-summaries", s.perm(auth.PermSettingsManage), gone(""))
	protected.GET("/operations/:id/activity", s.perm(auth.PermLibraryView), activityH.ListOperationActivity)
	// A read, despite the verb: the body carries a group's member ids, which can
	// be several hundred ULIDs. Gated like the GET above.
	protected.POST("/operations/activity/merged", s.perm(auth.PermLibraryView), activityH.ListMergedOperationActivity)

	// Split-book dedup
	protected.POST("/dedup/split-book-scan", s.perm(auth.PermScanTrigger), splitBookH.TriggerSplitBookScan)
	protected.GET("/dedup/split-book-candidates", s.perm(auth.PermLibraryView), splitBookH.ListSplitBookCandidates)
	protected.POST("/dedup/split-book-candidates/:id/merge", s.perm(auth.PermLibraryEditMetadata), splitBookH.MergeSplitBookCandidate)
	protected.POST("/dedup/split-book-candidates/bulk-merge", s.perm(auth.PermLibraryEditMetadata), splitBookH.BulkMergeSplitBookCandidates)

	// Filesystem + import paths
	protected.GET("/filesystem/home", s.perm(auth.PermSettingsManage), filesystemH.GetHomeDirectory)
	protected.GET("/filesystem/browse", s.perm(auth.PermSettingsManage), filesystemH.BrowseFilesystem)
	protected.POST("/filesystem/exclude", s.perm(auth.PermSettingsManage), filesystemH.CreateExclusion)
	protected.DELETE("/filesystem/exclude", s.perm(auth.PermSettingsManage), filesystemH.RemoveExclusion)
	protected.GET("/import-paths", s.perm(auth.PermSettingsManage), filesystemH.ListImportPaths)
	s.credRoute(protected, http.MethodPost, "/import-paths", s.perm(auth.PermSettingsManage), filesystemH.AddImportPath)
	protected.DELETE("/import-paths/:id", s.perm(auth.PermSettingsManage), filesystemH.RemoveImportPath)
	protected.POST("/import/file", s.perm(auth.PermScanTrigger), filesystemH.ImportFile)

	// Organize
	protected.GET("/audiobooks/:id/preview-organize", s.perm(auth.PermLibraryOrganize), organizeH.PreviewOrganize)
	protected.POST("/audiobooks/:id/organize", s.perm(auth.PermLibraryOrganize), organizeH.OrganizeBook)

	// Metadata cache
	protected.GET("/audiobooks/metadata/cached", s.perm(auth.PermLibraryView), metaCacheH.ListCachedCandidates)
	protected.GET("/audiobooks/metadata/cache/review", s.perm(auth.PermLibraryView), metaCacheH.GetCacheReviewResults)
	protected.POST("/audiobooks/metadata/batch-apply-cached", s.perm(auth.PermLibraryEditMetadata), metaCacheH.BatchApplyFromCache)
	protected.POST("/audiobooks/:id/clear-no-match", s.perm(auth.PermLibraryEditMetadata), metaCacheH.ClearMetadataNoMatch)

	// Reading progress. Canonical noun is /audiobooks/, matching every other
	// per-book route; /books/:id/... is kept registered as a DEPRECATED alias
	// to the same handlers so existing callers keep working (naming-audit
	// 2026-09-25, class 1: docs/audits/2026-09-25-interface-naming-consistency.md).
	protected.POST("/audiobooks/:id/position", readingH.SetPosition)
	protected.GET("/audiobooks/:id/position", readingH.GetPosition)
	protected.GET("/audiobooks/:id/state", readingH.GetBookState)
	protected.PATCH("/audiobooks/:id/status", readingH.SetBookStatus)
	protected.DELETE("/audiobooks/:id/status", readingH.ClearBookStatus)
	protected.POST("/audiobooks/:id/status/repair", readingH.RepairBookStatus)
	// Deprecated aliases — DO NOT remove without a deprecation window; kept
	// for backward compatibility per naming-audit class 1.
	protected.POST("/books/:id/position", readingH.SetPosition)
	protected.GET("/books/:id/position", readingH.GetPosition)
	protected.GET("/books/:id/state", readingH.GetBookState)
	protected.PATCH("/books/:id/status", readingH.SetBookStatus)
	protected.DELETE("/books/:id/status", readingH.ClearBookStatus)
	protected.POST("/books/:id/status/repair", readingH.RepairBookStatus)
	protected.GET("/me/:status", readingH.ListByStatus)

	// Playlists
	protected.GET("/playlists", s.perm(auth.PermLibraryView), playlistH.ListPlaylists)
	protected.POST("/playlists", playlistH.CreatePlaylist)
	protected.GET("/playlists/:id", playlistH.GetPlaylist)
	protected.PUT("/playlists/:id", playlistH.UpdatePlaylist)
	protected.DELETE("/playlists/:id", playlistH.DeletePlaylist)
	protected.POST("/playlists/:id/books", playlistH.AddBooksToPlaylist)
	protected.DELETE("/playlists/:id/books/:bookID", playlistH.RemoveBookFromPlaylist)
	protected.POST("/playlists/:id/reorder", playlistH.ReorderPlaylist)
	protected.POST("/playlists/:id/materialize", playlistH.MaterializePlaylist)
	// GET /playlists/:id/export.m3u had no caller in the repo or in AudioBooth
	// (01-P81c). It answers 410 Gone for one release (owner decision D9) and is
	// then removed. It stays on the protected group, so an unauthenticated
	// caller still gets 401 and only a signed-in caller learns it is gone.
	protected.GET("/playlists/:id/export.m3u", gone(""))

	// Collections. This is the ONLY surface that can create a dynamic
	// (query-backed) collection: Audiobookshelf has no such concept, so its
	// create payload has nowhere to put a query and everything made through the
	// ABS routes is static.
	//
	// 🔴 EVERY WRITE IS GATED, unlike the playlist block directly above, where
	// only the list carries s.perm and POST/PUT/DELETE carry nothing. That is not
	// an inconsistency to tidy up later: collections are server-wide, so an
	// ungated write here would let any authenticated user rewrite what every
	// other user sees. The ABS surface enforces the same permission, and it has
	// to be both — anyone locked out on one surface could otherwise just call the
	// other.
	//
	// Reads are open to any authenticated caller by design: server-wide means
	// everyone sees them.
	//
	// The two /books routes add and remove members of a STATIC collection through
	// the store's locked operations (D77); a dynamic collection answers 409. They
	// carry the same gate as every other write here.
	protected.GET("/collections", s.perm(auth.PermLibraryView), collectionH.ListCollections)
	protected.GET("/collections/:id", s.perm(auth.PermLibraryView), collectionH.GetCollection)
	protected.POST("/collections", s.perm(auth.PermCollectionsManage), collectionH.CreateCollection)
	protected.PUT("/collections/:id", s.perm(auth.PermCollectionsManage), collectionH.UpdateCollection)
	protected.DELETE("/collections/:id", s.perm(auth.PermCollectionsManage), collectionH.DeleteCollection)
	protected.POST("/collections/:id/materialize", s.perm(auth.PermCollectionsManage), collectionH.MaterializeCollection)
	protected.POST("/collections/:id/books", s.perm(auth.PermCollectionsManage), collectionH.AddBooksToCollection)
	protected.DELETE("/collections/:id/books/:bookId", s.perm(auth.PermCollectionsManage), collectionH.RemoveBookFromCollection)

	// User management
	users := protected.Group("/users")
	{
		users.GET("", s.perm("users.manage"), userH.ListUsers)
		// s.credRoute: no API key may invite (create) a user, change a
		// user's status or mint a reset link (a session). See
		// docs/plans/2026-10-07-apikey-expiry-and-privilege.md.
		s.credRoute(users, http.MethodPost, "/invite", s.perm("users.manage"), userH.CreateInvite)
		users.GET("/invites", s.perm("users.manage"), userH.ListInvites)
		users.DELETE("/invites/:token", s.perm("users.manage"), userH.DeleteInvite)
		s.credRoute(users, http.MethodPost, "/:id/deactivate", s.perm("users.manage"), userH.DeactivateUser)
		s.credRoute(users, http.MethodPost, "/:id/reactivate", s.perm("users.manage"), userH.ReactivateUser)
		s.credRoute(users, http.MethodPost, "/:id/reset-password", s.perm("users.manage"), s.handleResetPassword)
	}

	// Version groups
	protected.GET("/audiobooks/:id/versions", s.perm(auth.PermLibraryView), versionsH.ListAudiobookVersions)
	protected.POST("/audiobooks/:id/versions", s.perm(auth.PermLibraryEditMetadata), versionsH.LinkAudiobookVersion)
	protected.PUT("/audiobooks/:id/set-primary", s.perm(auth.PermLibraryEditMetadata), versionsH.SetAudiobookPrimary)
	protected.POST("/audiobooks/:id/split-version", s.perm(auth.PermLibraryEditMetadata), versionsH.SplitVersion)
	protected.POST("/audiobooks/:id/split-to-books", s.perm(auth.PermLibraryEditMetadata), versionsH.SplitSegmentsToBooks)
	protected.POST("/audiobooks/:id/move-segments", s.perm(auth.PermLibraryEditMetadata), versionsH.MoveSegments)
	protected.GET("/version-groups/:id", s.perm(auth.PermLibraryView), versionsH.GetVersionGroup)

	// Admin-only Phase 2 routes
	adminOnly := protected.Group("")
	adminOnly.Use(servermiddleware.RequireAdmin())
	{
		adminOnly.GET("/cache/stats/keys", cacheH.HandleCacheKeysIntrospection)
		// POST /cache/invalidate (flush one or all in-memory caches) had no
		// caller in the repo and no replacement (01-P81g). It answers 410 Gone
		// for one release (owner decision D9) and is then removed. It stays on
		// the admin-only group, so an unauthenticated caller still gets 401, a
		// non-admin 403, and only an admin learns it is gone. Until a
		// registered op replaces it, flushing a stale cache takes a restart.
		adminOnly.POST("/cache/invalidate", gone(""))
		// POST /admin/recompact-digests only enqueued the
		// maintenance.recompact-activity-digests op, which POST /operations/v2
		// starts just as well, and it had no caller in the repo (01-P81g). It
		// answers 410 Gone for one release (owner decision D9) and is then
		// removed. It stays on the admin-only group, so an unauthenticated
		// caller still gets 401, a non-admin 403, and only an admin learns it
		// is gone.
		adminOnly.POST("/admin/recompact-digests",
			gone(`POST /api/v1/operations/v2 with {"def_id":"maintenance.recompact-activity-digests"}`))
	}
}
