// file: internal/server/deluge_discovery.go
// version: 3.1.0
// guid: e6f7a8b9-c0d1-2e3f-4a5b-6c7d8e9f0a1b
// last-edited: 2026-07-13
//
// Deluge label-based audiobook discovery — HTTP handlers.
//
// Discovery and matching service logic lives in internal/deluge/discovery.go.
// This file contains only the *Server HTTP handlers that delegate to that package.

package server

import (
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
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

	unimported, err := delugeclient.DiscoverUnimported(s.Store(), client, label)
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

// handleDelugeDiscoverImport triggers an import of a discovered torrent's
// content_path into the library. Reuses the existing ImportFile pipeline.
// POST /api/v1/deluge/discover/import
// Body: { "content_path": "/mnt/downloads/Dune by Frank Herbert", "torrent_hash": "abc123" }
func (s *Server) handleDelugeDiscoverImport(c *gin.Context) {
	if !config.AppConfig.DelugeDiscoveryEnabled {
		httputil.RespondWithForbidden(c, "deluge discovery is disabled (set deluge_discovery_enabled=true to enable)")
		return
	}
	var req struct {
		ContentPath string `json:"content_path" binding:"required"`
		TorrentHash string `json:"torrent_hash"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if s.importService == nil {
		httputil.RespondWithInternalError(c, "import service not initialized")
		return
	}

	resp, err := s.importService.ImportFile(&importer.ImportFileRequest{
		FilePath: req.ContentPath,
		Organize: false,
	})
	if err != nil {
		httputil.InternalError(c, "failed to import file", err)
		return
	}

	httputil.RespondWithCreated(c, struct {
		Book        any    `json:"book"`
		TorrentHash string `json:"torrent_hash"`
	}{Book: resp, TorrentHash: req.TorrentHash})
}

// handleDiscoveryImport triggers importToLibrary for all book_files that
// have a deluge_hash but have not yet been imported (imported_from_deluge_at IS NULL).
// This is the bulk-import trigger called from the Settings UI.
// POST /api/v1/discovery/import
// Optional body: { "dry_run": true, "max_books": 100 }
func (s *Server) handleDiscoveryImport(c *gin.Context) {
	var req struct {
		DryRun   bool `json:"dry_run"`
		MaxBooks int  `json:"max_books"`
	}
	_ = c.ShouldBindJSON(&req) // optional body

	store := s.Store()
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
		newPath, importErr := delugeclient.ImportToLibrary(&config.AppConfig, client, store, full)
		if importErr != nil {
			results = append(results, result{FileID: f.ID, Path: f.FilePath, Error: importErr.Error()})
			failed++
		} else {
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
