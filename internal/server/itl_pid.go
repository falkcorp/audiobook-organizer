// file: internal/server/itl_pid.go
// version: 1.0.0
// guid: d6b1f048-3e29-4a75-9c81-0f2a7b4c6e93
// last-edited: 2026-07-23
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

	if c.Query("dry_run") == "true" {
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
