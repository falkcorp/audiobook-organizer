// file: internal/server/wire_operations_routes.go
// version: 1.0.0
// guid: f6a7b8c9-d0e1-2345-fabc-678901234567
// last-edited: 2026-06-23

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	operations "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	"github.com/gin-gonic/gin"
)

// wireOperationsRoutes registers operations v2 and operations domain routes
// on the protected group. Handler instantiation stays in wireHandlers.
func (s *Server) wireOperationsRoutes(
	protected *gin.RouterGroup,
	opsV2H *handlers.OperationsV2Handler,
	operationsH *operations.Handler,
) {
	// Operations v2 (UOS-06)
	protected.GET("/operations/timeline", s.perm(auth.PermLibraryView), opsV2H.GetOperationTimeline)
	protected.GET("/operations/events", s.perm(auth.PermLibraryView), opsV2H.OperationsSSE)
	protected.GET("/operations/v2/:id", s.perm(auth.PermLibraryView), opsV2H.GetOperationV2)
	protected.DELETE("/operations/v2/:id", s.perm(auth.PermSettingsManage), opsV2H.CancelOperationV2)
	protected.POST("/operations/v2", s.perm(auth.PermScanTrigger), opsV2H.TriggerOperationV2)
	protected.GET("/op-defs", s.perm(auth.PermLibraryView), opsV2H.ListOpDefs)
	protected.GET("/op-defs/:id", s.perm(auth.PermLibraryView), opsV2H.GetOpDef)

	// Operations domain (migrated from server_lifecycle.go). Paths + permission
	// guards copied verbatim. These share the /operations path prefix with the
	// operations_v2 routes above (timeline/events/v2/op-defs) and the survivors
	// that stay in server_lifecycle.go (active/recent/reconcile/itunes-path-*/
	// cleanup-version-groups/results/file-ops) — all distinct method+path pairs,
	// all using the identical `:id` param name, so Gin registers them cleanly.
	protected.GET("/operations", s.perm(auth.PermLibraryView), operationsH.ListOperations)
	protected.GET("/operations/stale", s.perm(auth.PermLibraryView), operationsH.ListStaleOperations)
	protected.POST("/operations/scan", s.perm(auth.PermScanTrigger), operationsH.StartScan)
	protected.POST("/operations/organize", s.perm(auth.PermScanTrigger), operationsH.StartOrganize)
	protected.POST("/operations/transcode", s.perm(auth.PermScanTrigger), operationsH.StartTranscode)
	protected.POST("/operations/optimize", s.perm(auth.PermScanTrigger), operationsH.StartOptimize)
	protected.GET("/operations/:id/status", s.perm(auth.PermLibraryView), operationsH.GetOperationStatus)
	protected.GET("/operations/:id/logs", s.perm(auth.PermLibraryView), operationsH.GetOperationLogs)
	protected.GET("/operations/:id/result", s.perm(auth.PermLibraryView), operationsH.GetOperationResult)
	protected.DELETE("/operations/:id", s.perm(auth.PermSettingsManage), operationsH.CancelOperation)
	protected.POST("/operations/clear-stale", s.perm(auth.PermSettingsManage), operationsH.ClearStaleOperations)
	protected.DELETE("/operations/history", s.perm(auth.PermSettingsManage), operationsH.DeleteOperationHistory)
	protected.POST("/operations/optimize-database", s.perm(auth.PermSettingsManage), operationsH.OptimizeDatabase)
	protected.POST("/operations/sweep-tombstones", s.perm(auth.PermSettingsManage), operationsH.SweepTombstones)
	protected.POST("/operations/set-internal-flag", s.perm(auth.PermSettingsManage), operationsH.SetInternalFlag)
	protected.GET("/operations/audit-files", s.perm(auth.PermSettingsManage), operationsH.AuditFileConsistency)
	protected.GET("/operations/:id/changes", s.perm(auth.PermLibraryView), operationsH.GetOperationChanges)
	protected.GET("/operations/:id/undo/preflight", s.perm(auth.PermLibraryView), operationsH.UndoPreflightHandler)
	protected.POST("/operations/:id/revert", s.perm(auth.PermLibraryOrganize), operationsH.RevertOperation)
	protected.GET("/tasks", s.perm(auth.PermSettingsManage), operationsH.ListTasks)
	protected.POST("/tasks/:name/run", s.perm(auth.PermSettingsManage), operationsH.RunTask)
	protected.PUT("/tasks/:name", s.perm(auth.PermSettingsManage), operationsH.UpdateTaskConfig)
	protected.POST("/maintenance-window/run", s.perm(auth.PermSettingsManage), operationsH.RunMaintenanceWindowNow)
	protected.GET("/maintenance-window/status", s.perm(auth.PermSettingsManage), operationsH.GetMaintenanceWindowStatus)
	protected.PUT("/maintenance-window/config", s.perm(auth.PermSettingsManage), operationsH.UpdateMaintenanceWindowConfig)
}
