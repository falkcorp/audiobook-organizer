// file: internal/server/itl_cleanup.go
// version: 2.0.0
// guid: 1e8b3d47-6a02-4c9f-b73e-2d5a0f8c1b96
// last-edited: 2026-09-10
//
// P3 merged-track cleanup handler: measures stale duplicate audiobook tracks
// left in the library by merged/superseded books. dry_run=true previews the
// removal set (candidates are sourced only from DB book_files, never
// music/podcast tracks). The apply path is permanently retired — see
// cleanupMergedHandler's doc comment below for why.

package server

import (
	"fmt"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/gin-gonic/gin"
)

// cleanupMergedApplyRetiredMessage is returned verbatim by the retired apply
// path of POST /api/v1/itunes/cleanup-merged.
const cleanupMergedApplyRetiredMessage = "cleanup-merged apply is retired (P3 decision: MEASURE-AND-STOP); dry_run=true only"

// cleanupMergedHandler handles POST /api/v1/itunes/cleanup-merged.
//
// Query param: dry_run=true returns the superseded-track removal preview
// without applying — this measurement capability stays live for future
// re-derivation.
//
// The apply path (dry_run=false, or the param omitted) is permanently
// retired and structurally can never reach the ITL writeback pipeline in
// internal/itunes/service: the owner's P0 cleanup provenance census over
// all 97,999 .itl tracks found SHA-gated
// removable=0 (docs/specs/2026-07-23-itunes-2way-p0-findings.md §F4) — there
// is nothing safe to remove today — and this handler's removal criterion
// (IsPrimaryVersion==false, internal/itunes/cleanup_merged.go) is separately
// flagged unsafe (TODO.md, iTunes 2-way-sync P3 item) because it can delete
// real chapter files, not just true duplicates. The refusal is returned
// before any ITL path resolution or file access (ComputeMergedTrackCleanup
// parses the .itl file), so a missing or unreadable .itl file can never turn
// the refusal into a 500 — an apply request is refused, never "failed".
func (s *Server) cleanupMergedHandler(c *gin.Context) {
	if c.Query("dry_run") != "true" {
		httputil.RespondWithSuccess(c, http.StatusGone, gin.H{
			"applied": false,
			"error":   cleanupMergedApplyRetiredMessage,
		})
		return
	}

	itlPath, ok := resolveITLWritePath(c)
	if !ok {
		return
	}

	store := s.storeForWiring()
	_, preview, err := itunes.ComputeMergedTrackCleanup(store, itlPath)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("cleanup-merged diff failed: %v", err))
		return
	}

	httputil.RespondWithOK(c, gin.H{"dry_run": true, "preview": preview})
}
