// file: internal/server/wire_media_routes.go
// version: 1.0.0
// guid: c9d0e1f2-a3b4-5678-cdef-901234567890
// last-edited: 2026-06-23

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	toolshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/tools"
	"github.com/gin-gonic/gin"
)

// wireMediaRoutes registers iTunes, AI, diagnostics, tools, and plugins routes
// on the protected group. Handler instantiation stays in wireHandlers.
func (s *Server) wireMediaRoutes(
	protected *gin.RouterGroup,
	itunesH *handlers.ITunesHandler,
	aiH *handlers.AIHandler,
	diagH *handlers.DiagnosticsHandler,
	toolsH *toolshandler.Handler,
	pluginsH *handlers.PluginsHandler,
) {
	// iTunes (12 migrated routes; survivors stay in server_lifecycle.go).
	// Two protected.Group("/itunes") blocks (here + survivors) is fine in Gin
	// since there is no duplicate method+path.
	itunesG := protected.Group("/itunes")
	{
		itunesG.POST("/validate", s.perm(auth.PermLibraryEditMetadata), itunesH.Validate)
		itunesG.POST("/test-mapping", s.perm(auth.PermLibraryEditMetadata), itunesH.TestMapping)
		itunesG.POST("/import", s.perm(auth.PermLibraryEditMetadata), itunesH.Import)
		itunesG.POST("/write-back", s.perm(auth.PermLibraryEditMetadata), itunesH.WriteBack)
		itunesG.POST("/write-back-all", s.perm(auth.PermLibraryEditMetadata), itunesH.WriteBackAll)
		itunesG.GET("/library-stats", s.perm(auth.PermLibraryView), itunesH.LibraryStats)
		itunesG.POST("/write-back/preview", s.perm(auth.PermLibraryEditMetadata), itunesH.WriteBackPreview)
		itunesG.GET("/books", s.perm(auth.PermLibraryView), itunesH.ListBooks)
		itunesG.GET("/import-status/:id", s.perm(auth.PermLibraryView), itunesH.ImportStatus)
		itunesG.POST("/import-status/bulk", s.perm(auth.PermLibraryEditMetadata), itunesH.ImportStatusBulk)
		itunesG.GET("/library-status", s.perm(auth.PermLibraryView), itunesH.LibraryStatus)
		itunesG.POST("/sync", s.perm(auth.PermLibraryEditMetadata), itunesH.Sync)
	}

	// AI domain (migrated from server_lifecycle.go).
	protected.POST("/authors/duplicates/ai-review", s.perm(auth.PermLibraryEditMetadata), aiH.ReviewDuplicateAuthors)
	protected.POST("/authors/duplicates/ai-review/apply", s.perm(auth.PermLibraryEditMetadata), aiH.ApplyAuthorReview)
	protected.POST("/ai/parse-filename", s.perm(auth.PermLibraryEditMetadata), aiH.ParseFilename)
	protected.POST("/ai/test-connection", s.perm(auth.PermLibraryEditMetadata), aiH.TestConnection)
	aiScans := protected.Group("/ai/scans")
	{
		aiScans.POST("", s.perm(auth.PermLibraryEditMetadata), aiH.StartScan)
		aiScans.GET("", s.perm(auth.PermLibraryView), aiH.ListScans)
		aiScans.GET("/compare", aiH.CompareScans) // Must be before /:id to avoid conflict
		aiScans.GET("/:id", s.perm(auth.PermLibraryView), aiH.GetScan)
		aiScans.GET("/:id/results", s.perm(auth.PermLibraryView), aiH.GetScanResults)
		aiScans.POST("/:id/apply", s.perm(auth.PermLibraryEditMetadata), aiH.ApplyScanResults)
		aiScans.POST("/:id/cancel", s.perm(auth.PermLibraryEditMetadata), aiH.CancelScan)
		aiScans.DELETE("/:id", s.perm(auth.PermLibraryDelete), aiH.DeleteScan)
	}
	protected.POST("/metadata-sources/test", s.perm(auth.PermSettingsManage), aiH.TestMetadataSource)
	protected.POST("/audiobooks/:id/parse-with-ai", s.perm(auth.PermLibraryEditMetadata), aiH.ParseAudiobook)
	protected.GET("/ai-jobs", s.perm(auth.PermSettingsManage), aiH.ListAIJobs)

	// Diagnostics (migrated from server_lifecycle.go).
	protected.GET("/diagnostics/db-health", s.perm(auth.PermSettingsManage), diagH.GetDBHealth)
	protected.POST("/diagnostics/export", s.perm(auth.PermSettingsManage), diagH.StartExport)
	protected.GET("/diagnostics/export/:operationId/download", s.perm(auth.PermSettingsManage), diagH.DownloadExport)
	protected.POST("/diagnostics/submit-ai", s.perm(auth.PermSettingsManage), diagH.SubmitAI)
	protected.GET("/diagnostics/ai-results/:operationId", s.perm(auth.PermSettingsManage), diagH.GetAIResults)
	protected.POST("/diagnostics/apply-suggestions", s.perm(auth.PermSettingsManage), diagH.ApplySuggestions)

	// Tools lifecycle
	protected.GET("/tools", s.perm(auth.PermSettingsManage), toolsH.List)
	protected.GET("/tools/:name/status", s.perm(auth.PermSettingsManage), toolsH.Status)
	protected.POST("/tools/:name/install", s.perm(auth.PermSettingsManage), toolsH.Install)

	// Plugins
	plugins := protected.Group("/plugins")
	{
		plugins.GET("", s.perm(auth.PermSettingsManage), pluginsH.ListPlugins)
		plugins.GET("/:id", s.perm(auth.PermSettingsManage), pluginsH.GetPlugin)
		plugins.POST("/:id/enable", s.perm(auth.PermSettingsManage), pluginsH.EnablePlugin)
		plugins.POST("/:id/disable", s.perm(auth.PermSettingsManage), pluginsH.DisablePlugin)
		plugins.GET("/:id/health", s.perm(auth.PermSettingsManage), pluginsH.PluginHealth)
		plugins.PUT("/:id/settings", s.perm(auth.PermSettingsManage), pluginsH.UpdatePluginSettings)
	}
}
