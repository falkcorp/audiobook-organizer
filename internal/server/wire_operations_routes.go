// file: internal/server/wire_operations_routes.go
// version: 1.2.0
// guid: f6a7b8c9-d0e1-2345-fabc-678901234567
// last-edited: 2026-08-26

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
	schedulerH *handlers.SchedulerHandler,
) {
	// Operations v2 (UOS-06)
	protected.GET("/operations/timeline", s.perm(auth.PermLibraryView), opsV2H.GetOperationTimeline)
	protected.GET("/operations/events", s.perm(auth.PermLibraryView), opsV2H.OperationsSSE)
	protected.GET("/operations/v2/:id/logs/download", s.perm(auth.PermLibraryView), opsV2H.DownloadOperationLogs)
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
	// RETIRED 2026-08-16: GET /operations, /operations/:id/status,
	// /operations/:id/logs.
	//
	// All three read the legacy `operations` table, which never transitions rows
	// out of `pending`: read against production 2026-08-16 it reported 183 of 200
	// rows pending, some six days old, while the v2 record for the same window
	// showed 179 completed, 13 interrupted_dropped, 6 canceled, 1 failed. The list
	// route had no caller at all; status and logs tried v2 first and fell back to
	// that table, so anything falling through got a confidently wrong answer.
	//
	// Replaced by GET /operations/timeline and GET /operations/v2/:id, the latter
	// now taking ?limit= so it covers the old ?tail= too.
	// RETIRED 2026-08-16: POST /operations/{scan,organize,transcode,optimize}.
	//
	// All four were pure shims — the handler body was a single launchOp() call
	// forwarding to registry.EnqueueOp, which is exactly what POST /operations/v2
	// does. They added no behaviour, only a second response shape, and that shape
	// was wrong: they answered 202 {"op_id":...,"id":...} unwrapped while the web
	// client read `.data.id`, so `op.id` threw, the caller's catch swallowed it,
	// and the UI said "Failed to start scan" WHILE THE SCAN WAS RUNNING.
	//
	// Callers now POST /operations/v2 {def_id: "library.scan", params: {...}}.
	// RETIRED 2026-08-17: DELETE /operations/:id.
	//
	// It differed from DELETE /operations/v2/:id in one way that mattered: it
	// resolved an AI scan by matching the id against each scan's OperationID and
	// cancelled it through the pipeline manager, which the registry cannot do.
	// That branch was ported to CancelOperationV2 FIRST — deleting this route
	// while v2 still went straight to registry.Cancel would have left the cancel
	// button answering 204 while the scan ran on.
	//
	// Its other behaviour is deliberately not carried over: on a registry miss it
	// force-marked the LEGACY row canceled, which the scheduler no longer writes
	// and nothing reads.

	// ── still on the legacy handler; each needs a v2 home before it can go ──
	protected.GET("/operations/stale", s.perm(auth.PermLibraryView), operationsH.ListStaleOperations)
	protected.GET("/operations/:id/result", s.perm(auth.PermLibraryView), operationsH.GetOperationResult)
	protected.POST("/operations/clear-stale", s.perm(auth.PermSettingsManage), operationsH.ClearStaleOperations)
	protected.DELETE("/operations/history", s.perm(auth.PermSettingsManage), operationsH.DeleteOperationHistory)
	protected.POST("/operations/optimize-database", s.perm(auth.PermSettingsManage), operationsH.OptimizeDatabase)
	protected.POST("/operations/sweep-tombstones", s.perm(auth.PermSettingsManage), operationsH.SweepTombstones)
	protected.POST("/operations/set-internal-flag", s.perm(auth.PermSettingsManage), operationsH.SetInternalFlag)
	protected.GET("/operations/audit-files", s.perm(auth.PermSettingsManage), operationsH.AuditFileConsistency)
	protected.GET("/operations/:id/changes", s.perm(auth.PermLibraryView), operationsH.GetOperationChanges)
	protected.GET("/operations/:id/undo/preflight", s.perm(auth.PermLibraryView), operationsH.UndoPreflightHandler)
	protected.POST("/operations/:id/revert", s.perm(auth.PermLibraryOrganize), operationsH.RevertOperation)

	// Task-scheduler and maintenance-window routes: scheduler configuration and
	// control, not v1 operation records, so they live on their own
	// SchedulerHandler (TODO.md scheduler-config item) rather than operationsH.
	// Paths, methods, and permission guards are unchanged from before the move.
	protected.GET("/tasks", s.perm(auth.PermSettingsManage), schedulerH.ListTasks)
	protected.POST("/tasks/:name/run", s.perm(auth.PermSettingsManage), schedulerH.RunTask)
	protected.PUT("/tasks/:name", s.perm(auth.PermSettingsManage), schedulerH.UpdateTaskConfig)
	protected.POST("/maintenance-window/run", s.perm(auth.PermSettingsManage), schedulerH.RunMaintenanceWindowNow)
	protected.GET("/maintenance-window/status", s.perm(auth.PermSettingsManage), schedulerH.GetMaintenanceWindowStatus)
	protected.PUT("/maintenance-window/config", s.perm(auth.PermSettingsManage), schedulerH.UpdateMaintenanceWindowConfig)
}
