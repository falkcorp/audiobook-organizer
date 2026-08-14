// file: internal/server/itl_pid.go
// version: 1.1.0
// guid: d6b1f048-3e29-4a75-9c81-0f2a7b4c6e93
// last-edited: 2026-08-14
//
// book_file iTunes-PID integrity endpoints. /pid-integrity is a read-only census
// of duplicate PIDs (a PID must identify exactly one book_file). /pid-repair
// backfills the duplicates found by the census: it keeps the PID on one canonical
// row and CLEARS it from the rest — never deleting a row or touching an audio file.
// Apply is dry-run-gated (dry_run=true → preview only). See
// docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md.

package server

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/gin-gonic/gin"
)

// pidIntegrityHandler handles GET/POST /api/v1/itunes/pid-integrity. Always
// read-only: it reports the duplicate-PID census + relocate-correctness probe.
func (s *Server) pidIntegrityHandler(c *gin.Context) {
	itlPath, _ := resolveITLWritePath(c) // best-effort: census works without the ITL
	report, err := itunes.ComputePIDIntegrity(s.Store(), itlPath)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("pid-integrity census failed: %v", err))
		return
	}
	httputil.RespondWithOK(c, gin.H{"report": report})
}

// pidRepairDryRun reports whether the request asks for a preview. dry_run has
// historically been a QUERY parameter only, but callers keep sending it as a
// JSON body ({"dry_run":true}) — which was silently ignored, so a request that
// asked for a preview took the APPLY path (fired on prod 2026-08-14; harmless
// only because the plan had files_to_clear=0). Honor both transports and fail
// toward preview: either source saying true means no writes.
func pidRepairDryRun(c *gin.Context) bool {
	if c.Query("dry_run") == "true" {
		return true
	}
	var body struct {
		DryRun bool `json:"dry_run"`
	}
	if err := c.ShouldBindJSON(&body); err == nil && body.DryRun {
		return true
	}
	return false
}

// pidRepairHandler handles POST /api/v1/itunes/pid-repair. dry_run=true returns the
// repair plan preview; otherwise it clears the redundant PID copies. Destructive to
// the itunes_persistent_id FIELD only (no row/file deletion) → guarded by dry_run.
func (s *Server) pidRepairHandler(c *gin.Context) {
	itlPath, ok := resolveITLWritePath(c)
	if !ok {
		return
	}
	groups, preview, err := itunes.ComputePIDRepairPlan(s.Store(), itlPath, itlPathMappings())
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("pid-repair plan failed: %v", err))
		return
	}

	if pidRepairDryRun(c) {
		httputil.RespondWithOK(c, gin.H{"dry_run": true, "preview": preview})
		return
	}

	result, err := itunes.ApplyPIDRepairPlan(s.Store(), groups)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("pid-repair apply failed: %v", err))
		return
	}
	slog.Info("ITL pid-repair applied", "groupsRepaired", result.GroupsRepaired,
		"filesCleared", result.FilesCleared, "errors", result.Errors)
	httputil.RespondWithOK(c, gin.H{"applied": true, "preview": preview, "result": result})
}
