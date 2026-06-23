// file: internal/server/wire_library_routes.go
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f23456789012
// last-edited: 2026-06-23

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// wireLibraryRoutes registers cache, activity, split-book, filesystem, organize,
// metadata-cache, reading, playlists, user-management, version-groups, and
// admin-only routes on the protected group.
func (s *Server) wireLibraryRoutes(
	protected *gin.RouterGroup,
	cacheH *handlers.CacheHandler,
	activityH *handlers.ActivityHandler,
	splitBookH *handlers.SplitBookHandler,
	filesystemH *handlers.FilesystemHandler,
	organizeH *handlers.OrganizeHandler,
	metaCacheH *handlers.MetadataCacheHandler,
	readingH *handlers.ReadingHandler,
	playlistH *handlers.PlaylistHandler,
	userH *handlers.UserHandler,
	versionsH *handlers.VersionsHandler,
) {
	// Cache stats (operational metrics — auth-gated per SEC-7)
	protected.GET("/cache/stats", s.perm(auth.PermLibraryView), cacheH.HandleCacheStats)
	protected.GET("/cache/stats/history", s.perm(auth.PermLibraryView), cacheH.HandleCacheStatsHistory)

	// Activity log
	protected.GET("/activity", s.perm(auth.PermLibraryView), activityH.ListActivity)
	protected.GET("/activity/sources", s.perm(auth.PermLibraryView), activityH.ListActivitySources)
	protected.POST("/activity/compact", s.perm(auth.PermSettingsManage), activityH.CompactActivity)
	protected.GET("/operations/:id/activity", s.perm(auth.PermLibraryView), activityH.ListOperationActivity)

	// Split-book dedup
	protected.POST("/dedup/split-book-scan", s.perm(auth.PermScanTrigger), splitBookH.TriggerSplitBookScan)
	protected.GET("/dedup/split-book-candidates", s.perm(auth.PermLibraryView), splitBookH.ListSplitBookCandidates)
	protected.POST("/dedup/split-book-candidates/:id/merge", s.perm(auth.PermLibraryEditMetadata), splitBookH.MergeSplitBookCandidate)

	// Filesystem + import paths
	protected.GET("/filesystem/home", s.perm(auth.PermSettingsManage), filesystemH.GetHomeDirectory)
	protected.GET("/filesystem/browse", s.perm(auth.PermSettingsManage), filesystemH.BrowseFilesystem)
	protected.POST("/filesystem/exclude", s.perm(auth.PermSettingsManage), filesystemH.CreateExclusion)
	protected.DELETE("/filesystem/exclude", s.perm(auth.PermSettingsManage), filesystemH.RemoveExclusion)
	protected.GET("/import-paths", s.perm(auth.PermSettingsManage), filesystemH.ListImportPaths)
	protected.POST("/import-paths", s.perm(auth.PermSettingsManage), filesystemH.AddImportPath)
	protected.DELETE("/import-paths/:id", s.perm(auth.PermSettingsManage), filesystemH.RemoveImportPath)
	protected.POST("/import/file", s.perm(auth.PermScanTrigger), filesystemH.ImportFile)

	// Organize + rename
	protected.POST("/audiobooks/:id/rename/preview", s.perm(auth.PermLibraryOrganize), organizeH.PreviewRename)
	protected.POST("/audiobooks/:id/rename/apply", s.perm(auth.PermLibraryOrganize), organizeH.ApplyRename)
	protected.GET("/audiobooks/:id/preview-organize", s.perm(auth.PermLibraryOrganize), organizeH.PreviewOrganize)
	protected.POST("/audiobooks/:id/organize", s.perm(auth.PermLibraryOrganize), organizeH.OrganizeBook)

	// Metadata cache
	protected.GET("/audiobooks/metadata/cached", s.perm(auth.PermLibraryView), metaCacheH.ListCachedCandidates)
	protected.GET("/audiobooks/metadata/cache/review", s.perm(auth.PermLibraryView), metaCacheH.GetCacheReviewResults)
	protected.POST("/audiobooks/metadata/batch-apply-cached", s.perm(auth.PermLibraryEditMetadata), metaCacheH.BatchApplyFromCache)
	protected.POST("/audiobooks/:id/clear-no-match", s.perm(auth.PermLibraryEditMetadata), metaCacheH.ClearMetadataNoMatch)

	// Reading progress
	protected.POST("/books/:id/position", readingH.SetPosition)
	protected.GET("/books/:id/position", readingH.GetPosition)
	protected.GET("/books/:id/state", readingH.GetBookState)
	protected.PATCH("/books/:id/status", readingH.SetBookStatus)
	protected.DELETE("/books/:id/status", readingH.ClearBookStatus)
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

	// User management
	users := protected.Group("/users")
	{
		users.GET("", s.perm("users.manage"), userH.ListUsers)
		users.POST("/invite", s.perm("users.manage"), userH.CreateInvite)
		users.GET("/invites", s.perm("users.manage"), userH.ListInvites)
		users.DELETE("/invites/:token", s.perm("users.manage"), userH.DeleteInvite)
		users.POST("/:id/deactivate", s.perm("users.manage"), userH.DeactivateUser)
		users.POST("/:id/reactivate", s.perm("users.manage"), userH.ReactivateUser)
		users.POST("/:id/reset-password", s.perm("users.manage"), userH.ResetPassword)
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
		adminOnly.POST("/admin/recompact-digests", activityH.RecompactDigests)
	}
}
