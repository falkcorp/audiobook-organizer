// file: internal/server/itl_relocate.go
// version: 1.0.0
// guid: 6a1d4e83-2c9f-4b07-9e15-8d3a0b6f2c41
// last-edited: 2026-07-22
//
// Location-only iTunes writeback ("2-way sync" audiobook relocate) HTTP handlers.
// Unlike /rebuild (DB-authoritative — removes every ITL track not in the DB, which
// would gut music/podcasts/playlists against a full library), /relocate emits
// ONLY per-file location patches keyed on each book_file's own persistent ID and
// never removes or adds a track. /adopt-base re-blesses the identity sidecar after
// the writeback slot is reseeded from a different library. See
// docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md.

package server

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
)

// resolveITLWritePath reads and validates the configured ITL write path, writing
// an error response and returning ok=false when it is unset/invalid.
func resolveITLWritePath(c *gin.Context) (string, bool) {
	rawPath := config.AppConfig.ITunes.LibraryWritePath
	if rawPath == "" {
		httputil.RespondWithBadRequest(c, "ITunesLibraryWritePath not configured")
		return "", false
	}
	itlPath, err := pathvalidation.CleanAbsolutePath(rawPath)
	if err != nil {
		httputil.RespondWithInternalError(c, "invalid ITunesLibraryWritePath in config")
		return "", false
	}
	return itlPath, true
}

// relocateITLHandler handles POST /api/v1/itunes/relocate.
// Query param: dry_run=true returns the location-only diff preview without
// applying. Otherwise applies the location patches via the SafeWriteITL pipeline.
func (s *Server) relocateITLHandler(c *gin.Context) {
	itlPath, ok := resolveITLWritePath(c)
	if !ok {
		return
	}

	store := s.Store()
	ops, preview, err := itunes.ComputeRelocateOps(store, itlPath, itlPathMappings())
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("relocate diff failed: %v", err))
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

	// Location-only op set: no adds, no removes → the track count is unchanged,
	// so the bounded-delta guard passes and every non-matched track (music,
	// podcasts) and all playlists are left byte-for-byte intact.
	if err := itunesservice.SafeWriteITL(itlPath, *ops); err != nil {
		httputil.RespondWithSuccess(c, http.StatusInternalServerError,
			gin.H{"applied": false, "preview": preview, "error": err.Error()})
		return
	}

	slog.Info("ITL relocate applied",
		"toRelocate", preview.ToRelocate, "matched", preview.Matched,
		"unmatched", preview.UnmatchedFiles, "unmappable", preview.Unmappable)
	httputil.RespondWithOK(c, gin.H{"applied": true, "preview": preview})
}

// adoptBaseHandler handles POST /api/v1/itunes/adopt-base. Rewrites the
// .identity.json sidecar to describe the library currently at the write path —
// run once after reseeding the writeback slot from a different library, else the
// stale sidecar trips the K13/K14 identity guards and rejects every write.
func (s *Server) adoptBaseHandler(c *gin.Context) {
	itlPath, ok := resolveITLWritePath(c)
	if !ok {
		return
	}
	id, err := itunes.AdoptLibraryIdentity(itlPath)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("adopt-base failed: %v", err))
		return
	}
	httputil.RespondWithOK(c, gin.H{"adopted": true, "identity": id})
}
