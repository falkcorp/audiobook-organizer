// file: internal/server/wire_system_routes.go
// version: 1.2.0
// guid: a7b8c9d0-e1f2-3456-abcd-789012345678
// last-edited: 2026-07-16

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	system "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// wireSystemRoutes registers the system domain routes on the protected group.
// Handler instantiation stays in wireHandlers.
func (s *Server) wireSystemRoutes(
	protected *gin.RouterGroup,
	systemH *system.Handler,
) {
	// System domain (migrated from server_lifecycle.go). Paths + permission
	// guards copied verbatim. The public /health (x3) and /api/events routes stay
	// in setupRoutes — they are registered on s.router BEFORE the /api/* redirect
	// middleware, so re-registering them here would change their middleware
	// ordering; they delegate to systemH via closures instead.
	protected.GET("/policy/tags", s.perm(auth.PermLibraryView), systemH.HandlePolicyTags)
	protected.GET("/system/status", s.perm(auth.PermSettingsManage), systemH.GetSystemStatus)
	protected.GET("/system/announcements", s.perm(auth.PermSettingsManage), systemH.GetSystemAnnouncements)
	protected.GET("/system/storage", s.perm(auth.PermSettingsManage), systemH.GetSystemStorage)
	protected.GET("/system/logs", s.perm(auth.PermSettingsManage), systemH.GetSystemLogs)
	protected.GET("/system/activity-log", s.perm(auth.PermSettingsManage), systemH.GetSystemActivityLog)
	// Destructive full-wipe endpoints: gate on RequireAdmin() to match the
	// sibling /maintenance/wipe (server_lifecycle.go) rather than the weaker
	// PermSettingsManage — a custom non-admin role granted settings.manage must
	// not be able to wipe the whole library via reset. Same admin-only subgroup
	// pattern used in server_lifecycle.go and wire_library_routes.go.
	adminOnly := protected.Group("")
	adminOnly.Use(servermiddleware.RequireAdmin())
	{
		adminOnly.POST("/system/reset", systemH.ResetSystem)
		adminOnly.POST("/system/factory-reset", systemH.FactoryReset)
	}
	protected.GET("/config", s.perm(auth.PermSettingsManage), systemH.GetConfig)
	protected.PUT("/config", s.perm(auth.PermSettingsManage), systemH.UpdateConfig)
	protected.GET("/dashboard", s.perm(auth.PermLibraryView), systemH.GetDashboard)
	protected.POST("/backup/create", s.perm(auth.PermSettingsManage), systemH.CreateBackup)
	protected.GET("/backup/list", s.perm(auth.PermSettingsManage), systemH.ListBackups)
	protected.POST("/backup/restore", s.perm(auth.PermSettingsManage), systemH.RestoreBackup)
	protected.DELETE("/backup/:filename", s.perm(auth.PermSettingsManage), systemH.DeleteBackup)
	protected.GET("/library/quick-queries", s.perm(auth.PermLibraryView), systemH.GetQuickQueries)
	protected.GET("/maintenance/transcribe-stats", s.perm(auth.PermLibraryView), systemH.GetTranscribeStats)
	protected.GET("/blocked-hashes", s.perm(auth.PermLibraryView), systemH.ListBlockedHashes)
	protected.POST("/blocked-hashes", s.perm(auth.PermLibraryEditMetadata), systemH.AddBlockedHash)
	protected.DELETE("/blocked-hashes/:hash", s.perm(auth.PermLibraryDelete), systemH.RemoveBlockedHash)
	protected.GET("/preferences/:key", s.perm(auth.PermLibraryView), systemH.GetUserPreference)
	protected.PUT("/preferences/:key", s.perm(auth.PermLibraryEditMetadata), systemH.SetUserPreference)
	protected.DELETE("/preferences/:key", s.perm(auth.PermLibraryDelete), systemH.DeleteUserPreference)
}
