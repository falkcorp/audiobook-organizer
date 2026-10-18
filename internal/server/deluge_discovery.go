// file: internal/server/deluge_discovery.go
// version: 3.8.0
// guid: e6f7a8b9-c0d1-2e3f-4a5b-6c7d8e9f0a1b
// last-edited: 2026-10-18
//
// Deluge label-based audiobook discovery — HTTP handlers.
//
// Discovery and matching service logic lives in internal/deluge/discovery.go.
// This file contains only the *Server HTTP handlers that delegate to that package.

package server

import (
	"errors"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/gin-gonic/gin"
)

// handleDelugeDiscover returns Deluge torrents not yet in the library.
// GET /api/v1/deluge/discover?label=audiobooks
func (s *Server) handleDelugeDiscover(c *gin.Context) {
	if !config.AppConfig.DelugeDiscoveryEnabled {
		httputil.RespondWithForbidden(c, "deluge discovery is disabled (set deluge_discovery_enabled=true to enable)")
		return
	}
	client := getDelugeClient()
	if client == nil {
		httputil.RespondWithBadRequest(c, "deluge not configured")
		return
	}

	label := c.Query("label")
	if label == "" {
		label = config.AppConfig.DelugeDiscoveryLabel
	}

	unimported, err := delugeclient.DiscoverUnimported(s.storeForWiring(), client, label)
	if err != nil {
		httputil.RespondWithError(c, http.StatusBadGateway, err.Error(), "BAD_GATEWAY")
		return
	}

	httputil.RespondWithOK(c, struct {
		Label      string                           `json:"label"`
		Candidates []delugeclient.DiscoveredTorrent `json:"candidates"`
		Count      int                              `json:"count"`
	}{Label: label, Candidates: unimported, Count: len(unimported)})
}

// handleDelugeListLabels returns all labels from the Deluge Label plugin.
// GET /api/v1/deluge/labels
func (s *Server) handleDelugeListLabels(c *gin.Context) {
	client := getDelugeClient()
	if client == nil {
		httputil.RespondWithBadRequest(c, "deluge not configured")
		return
	}
	labels, err := client.ListLabels()
	if err != nil {
		httputil.RespondWithError(c, http.StatusBadGateway, err.Error(), "BAD_GATEWAY")
		return
	}
	httputil.RespondWithOK(c, struct {
		Labels any `json:"labels"`
		Count  int `json:"count"`
	}{Labels: labels, Count: len(labels)})
}

// handleDiscoveryImport runs deluge.ImportToLibrary (internal/deluge/import.go)
// for every book_file that has a deluge_hash but has not yet been imported
// (imported_from_deluge_at IS NULL). A file already at its library
// destination is marked imported in place, not copied.
// This is the bulk-import trigger called from the Settings UI.
// POST /api/v1/discovery/import
// Optional body: { "dry_run": true, "max_books": 100 }
func (s *Server) handleDiscoveryImport(c *gin.Context) {
	var req struct {
		DryRun   bool `json:"dry_run"`
		MaxBooks int  `json:"max_books"`
	}
	_ = c.ShouldBindJSON(&req) // optional body

	store := s.storeForWiring()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	client := getDelugeClient()
	if client == nil {
		httputil.RespondWithServiceUnavailable(c, "deluge not configured")
		return
	}

	// Centralized store method — uses the memdb deluge_hash fastpath
	// when published (avoids loading all 308K BookFiles just to filter
	// to the small Deluge-touched subset).
	pending, err := store.GetBookFilesNeedingDelugeImportCore()
	if err != nil {
		httputil.InternalError(c, "failed to load book files", err)
		return
	}

	if req.MaxBooks > 0 && len(pending) > req.MaxBooks {
		pending = pending[:req.MaxBooks]
	}

	type result struct {
		FileID  string `json:"file_id"`
		Path    string `json:"path"`
		NewPath string `json:"new_path,omitempty"`
		Error   string `json:"error,omitempty"`
		// SkippedReason is set when nothing was imported and nothing failed.
		SkippedReason string `json:"skipped_reason,omitempty"`
	}

	var results []result
	imported, skipped, failed := 0, 0, 0

	for i := range pending {
		f := &pending[i]
		if req.DryRun {
			results = append(results, result{FileID: f.ID, Path: f.FilePath})
			skipped++
			continue
		}
		// Hydrate the full row before writing back — f is the Core (memdb-slim)
		// projection, and passing it straight to ImportToLibrary would silently
		// wipe the fingerprint-diagnostic fields on its UpdateBookFile call
		// (STOREFID PR-D).
		full, hydrateErr := store.GetBookFileByID(f.BookID, f.ID)
		if hydrateErr != nil || full == nil {
			errMsg := "book file not found"
			if hydrateErr != nil {
				errMsg = hydrateErr.Error()
			}
			results = append(results, result{FileID: f.ID, Path: f.FilePath, Error: errMsg})
			failed++
			continue
		}
		srcPath := full.FilePath // ImportToLibrary rewrites full.FilePath on success
		newPath, importErr := delugeclient.ImportToLibrary(&config.AppConfig, client, store, full, s.protectedChecker())
		switch {
		case errors.Is(importErr, tagger.ErrProtectedPathWrite):
			// A protected file that is its own library destination: there is
			// nowhere to copy it. Nothing was imported and nothing broke.
			results = append(results, result{FileID: f.ID, Path: f.FilePath, SkippedReason: importErr.Error()})
			skipped++
		case importErr != nil:
			// newPath is non-empty only when a copy was left in the library
			// unrecorded (see ImportToLibrary); report where it is.
			results = append(results, result{FileID: f.ID, Path: f.FilePath, NewPath: newPath, Error: importErr.Error()})
			failed++
		case newPath == srcPath:
			// Source and destination are the same file: no copy. The row is
			// marked imported so it stops coming back as pending. Until
			// 2026-09-14 this was counted as imported, with no row update.
			results = append(results, result{FileID: f.ID, Path: f.FilePath, SkippedReason: "file is already at its library destination; nothing was copied, and the row is marked imported"})
			skipped++
		default:
			results = append(results, result{FileID: f.ID, Path: f.FilePath, NewPath: newPath})
			imported++
		}
	}

	httputil.RespondWithOK(c, struct {
		Total    int      `json:"total"`
		Imported int      `json:"imported"`
		Skipped  int      `json:"skipped"`
		Failed   int      `json:"failed"`
		DryRun   bool     `json:"dry_run"`
		Results  []result `json:"results"`
	}{Total: len(pending), Imported: imported, Skipped: skipped, Failed: failed, DryRun: req.DryRun, Results: results})
}
