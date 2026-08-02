// file: internal/server/itl_cleanup.go
// version: 1.0.0
// guid: 1e8b3d47-6a02-4c9f-b73e-2d5a0f8c1b96
// last-edited: 2026-07-22
//
// P3 merged-track cleanup handler: removes stale duplicate audiobook tracks left
// in the library by merged/superseded books. Removal auto-cleans orphaned
// playlist references (RemoveTracksByPIDLE) and never targets music/podcast
// tracks (candidates are sourced only from DB book_files). dry_run=true previews.

package server

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/gin-gonic/gin"
)

// cleanupMergedHandler handles POST /api/v1/itunes/cleanup-merged.
// Query param: dry_run=true returns the superseded-track removal preview without
// applying. Otherwise removes the tracks via the SafeWriteITL pipeline.
func (s *Server) cleanupMergedHandler(c *gin.Context) {
	itlPath, ok := resolveITLWritePath(c)
	if !ok {
		return
	}

	store := s.Store()
	ops, preview, err := itunes.ComputeMergedTrackCleanup(store, itlPath)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("cleanup-merged diff failed: %v", err))
		return
	}

	if c.Query("dry_run") == "true" {
		httputil.RespondWithOK(c, gin.H{"dry_run": true, "preview": preview})
		return
	}

	if ops.IsEmpty() {
		httputil.RespondWithOK(c, gin.H{"applied": true, "preview": preview})
		return
	}

	// Removal excises the master tracks and cleans orphaned playlist refs. The
	// default contract's bounded-delta cap (max 5000 removes) is the safety net —
	// a larger removal is refused rather than force-applied.
	if err := itunesservice.SafeWriteITL(itlPath, *ops); err != nil {
		httputil.RespondWithSuccess(c, http.StatusInternalServerError,
			gin.H{"applied": false, "preview": preview, "error": err.Error()})
		return
	}

	slog.Info("ITL cleanup-merged applied", "toRemove", preview.ToRemove, "sharedSkipped", preview.SharedSkipped)
	httputil.RespondWithOK(c, gin.H{"applied": true, "preview": preview})
}
