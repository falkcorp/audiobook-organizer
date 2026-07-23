// file: internal/server/itl_rebuild.go
// version: 3.4.0
// guid: 8f7e6d5c-4b3a-2c1d-0e9f-8a7b6c5d4e3f
// last-edited: 2026-07-23
//
// iTunes library rebuild service: diffs the current DB state
// against the current ITL file and computes the minimal set of
// changes (adds, removes, metadata updates, location patches)
// to synchronize them. Changes are applied in one atomic
// ApplyITLOperations call through the existing itunesservice.SafeWriteITL
// pipeline (backup → validate → apply → validate → rollback on
// failure). Backlog 7.9 — "diff and batch" mode.
//
// This file is now a thin wrapper around the core rebuild logic in
// internal/itunes/rebuild.go.

package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
)

// ITLRebuildPreview summarizes the diff between the DB and the
// current ITL file without applying any changes. Returned by
// the dry-run path so the user can review before committing.
//
// Deprecated: Use itunes.ITLRebuildPreview instead.
type ITLRebuildPreview = itunes.ITLRebuildPreview

// ITLRebuildResult is the outcome of an applied rebuild.
//
// Deprecated: Use itunes.ITLRebuildResult instead.
type ITLRebuildResult = itunes.ITLRebuildResult

// itlPathMappings converts the configured iTunes path mappings into the itunes
// package type so the rebuild/export writeback can canonicalize each book's local
// FilePath into a native Windows ITL location. If the config is empty (the mapping
// hasn't been set), locations that aren't already native Windows paths are skipped
// rather than written raw — so the ITL never contains an invalid 0x0D.
func itlPathMappings() []itunes.PathMapping {
	cfg := config.AppConfig.ITunes.PathMappings
	out := make([]itunes.PathMapping, len(cfg))
	for i, m := range cfg {
		out[i] = itunes.PathMapping{From: m.From, To: m.To}
	}
	return out
}

// rebuildITLHandler handles POST /api/v1/itunes/rebuild.
// Query param: dry_run=true returns the diff preview without
// applying. Otherwise applies the diff via itunesservice.SafeWriteITL.
func (s *Server) rebuildITLHandler(c *gin.Context) {
	rawPath := config.AppConfig.ITunes.LibraryWritePath
	if rawPath == "" {
		httputil.RespondWithBadRequest(c, "ITunesLibraryWritePath not configured")
		return
	}
	itlPath, err := pathvalidation.CleanAbsolutePath(rawPath)
	if err != nil {
		httputil.RespondWithInternalError(c, "invalid ITunesLibraryWritePath in config")
		return
	}

	store := s.Store()
	ops, preview, err := itunes.ComputeITLDiff(store, itlPath, itlPathMappings())
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("diff failed: %v", err))
		return
	}

	dryRun := c.Query("dry_run") == "true"
	if dryRun {
		httputil.RespondWithOK(c, struct {
			DryRun  bool                      `json:"dry_run"`
			Preview *itunes.ITLRebuildPreview `json:"preview"`
		}{DryRun: true, Preview: preview})
		return
	}

	// Target-shape guard: refuse to gut a real library (music/podcasts/playlists)
	// with this DB-authoritative diff unless deliberately overridden.
	if _, gerr := itunes.GuardRebuildTarget(itlPath, c.Query("allow_full_library") == "true"); gerr != nil {
		httputil.RespondWithBadRequest(c, gerr.Error())
		return
	}

	// Apply.
	if ops.IsEmpty() {
		httputil.RespondWithOK(c, itunes.ITLRebuildResult{
			Preview: *preview,
			Applied: true,
		})
		return
	}

	if err := itunesservice.SafeWriteITL(itlPath, *ops); err != nil {
		httputil.RespondWithSuccess(c, http.StatusInternalServerError, itunes.ITLRebuildResult{
			Preview: *preview,
			Applied: false,
			Error:   err.Error(),
		})
		return
	}

	slog.Info("ITL rebuild removed , added , updated-meta , updated-loc", "toRemove", preview.ToRemove, "toAdd", preview.ToAdd, "toUpdateMeta", preview.ToUpdateMeta, "toUpdateLoc", preview.ToUpdateLoc)

	httputil.RespondWithOK(c, itunes.ITLRebuildResult{
		Preview: *preview,
		Applied: true,
	})
}

// rebuildITLFullHandler handles POST /api/v1/itunes/rebuild-full.
// Strips ALL tracks from the ITL and re-inserts every DB book with an iTunes PID.
// This is the "nuclear" reset path — use rebuildITLHandler (incremental diff) first.
// Query params: dry_run=true returns a preview without applying;
// acknowledge_shrink=true is the explicit K15 operator gate required when the
// rebuild would shrink the library by more than half (under-populated DB
// protection — without it such a rebuild is refused).
func (s *Server) rebuildITLFullHandler(c *gin.Context) {
	rawPath := config.AppConfig.ITunes.LibraryWritePath
	if rawPath == "" {
		httputil.RespondWithBadRequest(c, "ITunesLibraryWritePath not configured")
		return
	}
	itlPath, err := pathvalidation.CleanAbsolutePath(rawPath)
	if err != nil {
		httputil.RespondWithInternalError(c, "invalid ITunesLibraryWritePath in config")
		return
	}

	dryRun := c.Query("dry_run") == "true"
	store := s.Store()

	if dryRun {
		// Parse just to count tracks and books — don't apply.
		lib, err := itunes.ParseITL(itlPath)
		if err != nil {
			httputil.RespondWithInternalError(c, fmt.Sprintf("parse ITL: %v", err))
			return
		}
		preview := itunes.ITLRebuildPreview{
			TracksInITL: len(lib.Tracks),
		}
		httputil.RespondWithOK(c, struct {
			DryRun  bool                     `json:"dry_run"`
			Preview itunes.ITLRebuildPreview `json:"preview"`
		}{DryRun: true, Preview: preview})
		return
	}

	// Target-shape guard: /rebuild-full passes ForceContractConfig (bypasses
	// bounded-delta), so add an explicit fail-closed refusal when the target looks
	// like the real library — belt-and-suspenders over the K15 shrink gate.
	if _, gerr := itunes.GuardRebuildTarget(itlPath, c.Query("allow_full_library") == "true"); gerr != nil {
		httputil.RespondWithBadRequest(c, gerr.Error())
		return
	}

	result, err := itunes.RebuildITLFromDB(store, itlPath, itlPath, itlPathMappings(), c.Query("acknowledge_shrink") == "true")
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("full rebuild failed: %v", err))
		return
	}

	slog.Info("ITL full-rebuild removed existing tracks, inserted DB books", "toRemove", result.Preview.ToRemove, "toAdd", result.Preview.ToAdd)
	httputil.RespondWithOK(c, result)
}

// exportITLPartialHandler handles POST /api/v1/itunes/export-partial.
// Builds a partial ITL containing only the requested book IDs and returns
// it as a downloadable file. Body: {"book_ids": ["id1", "id2", ...]}
// If book_ids is empty or omitted, all primary-version books with PIDs are included.
func (s *Server) exportITLPartialHandler(c *gin.Context) {
	rawPath := config.AppConfig.ITunes.LibraryWritePath
	if rawPath == "" {
		httputil.RespondWithBadRequest(c, "ITunesLibraryWritePath not configured")
		return
	}
	itlPath, err := pathvalidation.CleanAbsolutePath(rawPath)
	if err != nil {
		httputil.RespondWithInternalError(c, "invalid ITunesLibraryWritePath in config")
		return
	}

	var body struct {
		BookIDs []string `json:"book_ids"`
	}
	_ = c.ShouldBindJSON(&body) // empty body = all books

	data, err := itunes.BuildExportITL(s.Store(), itlPath, body.BookIDs, itlPathMappings())
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("build export ITL: %v", err))
		return
	}

	filename := "iTunes Library Export " + time.Now().Format("2006-01-02") + filepath.Ext(itlPath)
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/octet-stream", data)
}
