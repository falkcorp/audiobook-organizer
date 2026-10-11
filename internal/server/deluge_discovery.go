// file: internal/server/deluge_discovery.go
// version: 3.7.0
// guid: e6f7a8b9-c0d1-2e3f-4a5b-6c7d8e9f0a1b
// last-edited: 2026-10-10
//
// Deluge label-based audiobook discovery — HTTP handlers.
//
// Discovery and matching service logic lives in internal/deluge/discovery.go.
// This file contains only the *Server HTTP handlers that delegate to that package.

package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
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

	// Resolve the torrent link BEFORE importing: a hash already linked to a
	// live book is refused (409), a hash that does not check out against
	// Deluge is rejected (400) or dropped (unlinked), and only a verified hash
	// is ever handed to ImportFile.
	verifiedHash, linkNote, ok := s.verifyDiscoveryTorrentHash(c, req.ContentPath, req.TorrentHash)
	if !ok {
		return
	}

	resp, err := s.importService.ImportFile(&importer.ImportFileRequest{
		FilePath:    req.ContentPath,
		Organize:    false,
		TorrentHash: verifiedHash,
	})
	if err != nil {
		// A doubled request passes the pre-check above twice (it runs outside
		// ImportFile's per-torrent lock); the second one is refused there,
		// before any book exists, and gets the same 409 as the sequential case.
		var linkedErr *importer.TorrentAlreadyLinkedError
		if errors.As(err, &linkedErr) {
			respondDiscoverAlreadyLinked(c, linkedErr.BookID)
			return
		}
		httputil.InternalError(c, "failed to import file", err)
		return
	}

	// torrent_linked reports what the importer actually wrote, not what was
	// asked for: a verified hash can still go unlinked (previously purged
	// content, a failed version or file row), and the importer's note says why.
	if linkNote == "" {
		linkNote = resp.TorrentLinkNote
	}
	httputil.RespondWithCreated(c, struct {
		Book            any    `json:"book"`
		TorrentHash     string `json:"torrent_hash"`
		TorrentLinked   bool   `json:"torrent_linked"`
		TorrentLinkNote string `json:"torrent_link_note,omitempty"`
	}{Book: resp, TorrentHash: req.TorrentHash, TorrentLinked: resp.TorrentLinked, TorrentLinkNote: linkNote})
}

// discoverVerifyTimeout bounds the Deluge round trip (login plus one status
// call) that verifies a discover-import hash. The client's own transport
// timeout is 5 minutes, long enough for a proxy in front of the server to give
// up first while the import still completes, inviting a retry that duplicates
// the book. A variable only so tests can shorten it.
var discoverVerifyTimeout = 15 * time.Second

// discoverImportLog carries the upstream error detail that the response note
// deliberately omits.
var discoverImportLog = logger.New("server.deluge-discover-import")

// Fixed notes for a hash dropped before the import. They never carry upstream
// error text: a transport error names the Deluge URL, an internal host.
const (
	discoverNoteNotConfigured = "deluge not configured"
	discoverNoteUnreachable   = "deluge unreachable"
	discoverNoteTimedOut      = "deluge timed out"
)

// discoverErrAlreadyLinked is the 409 message for a torrent a live book
// already holds; the response's book_id names that book.
const discoverErrAlreadyLinked = "torrent is already linked to a book in the library"

// respondDiscoverAlreadyLinked writes the 409 for a torrent a live book
// already holds. Both refusals use it, the pre-check in
// verifyDiscoveryTorrentHash and the one under ImportFile's per-torrent lock,
// so a caller sees one response whichever caught the duplicate.
func respondDiscoverAlreadyLinked(c *gin.Context, holderBookID string) {
	httputil.RespondWithErrorFields(c, http.StatusConflict, discoverErrAlreadyLinked, "TORRENT_ALREADY_LINKED",
		map[string]any{"book_id": holderBookID})
}

// verifyDiscoveryTorrentHash normalizes the caller's torrent hash and
// cross-checks it against Deluge. It returns the hash to store (empty when the
// import must proceed unlinked), a note explaining why it is empty, and
// ok=false when it has already written an error response.
//
// A hash that a live version already holds in the torrent index is refused
// with 409 before Deluge is asked anything and before any book is created:
// the torrent is already in the library, so the request is a retry or a
// duplicate, and importing it would create a second book whose link (were it
// written) would move the one-to-one index away from the first. The check
// runs whatever Deluge's state, because the duplicate book is the harm, not
// the link. A discover content_path is the whole torrent (save_path/name), so
// one torrent never legitimately feeds two single-file imports. ImportFile
// keeps its own guard, under a per-torrent lock, for any path that gets past
// this one (a concurrent pair, or another caller): it refuses with
// importer.ErrTorrentAlreadyLinked before creating a book, and the handler
// answers with the same 409.
//
// An unverifiable hash is dropped, never stored: Deluge not configured,
// unreachable or not answering within discoverVerifyTimeout proceeds unlinked
// with a fixed note (the upstream error is logged, never returned); a malformed hash, a torrent Deluge does not
// know, or a content_path that is not a file of that torrent is a 400.
func (s *Server) verifyDiscoveryTorrentHash(c *gin.Context, contentPath, rawHash string) (hash, note string, ok bool) {
	if rawHash == "" {
		return "", "", true
	}
	hash, err := delugeclient.NormalizeTorrentID(rawHash)
	if err != nil {
		httputil.RespondWithBadRequest(c, "torrent_hash is not a valid torrent id: "+err.Error())
		return "", "", false
	}
	if store := s.storeForWiring(); store != nil {
		holder, holdErr := versions.LiveTorrentHolder(store, hash)
		if holdErr != nil {
			httputil.InternalError(c, "failed to read the torrent index", holdErr)
			return "", "", false
		}
		if holder != nil {
			respondDiscoverAlreadyLinked(c, holder.BookID)
			return "", "", false
		}
	}
	client := getDelugeClient()
	if client == nil {
		return "", discoverNoteNotConfigured, true
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), discoverVerifyTimeout)
	defer cancel()
	detail, err := client.GetTorrentDetailContext(ctx, hash)
	if err != nil {
		if errors.Is(err, delugeclient.ErrTorrentNotFound) {
			httputil.RespondWithBadRequest(c, "torrent_hash does not name a torrent in Deluge")
			return "", "", false
		}
		note := discoverNoteUnreachable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			note = discoverNoteTimedOut
		}
		discoverImportLog.Warn("torrent %s could not be verified against Deluge (%s); importing unlinked: %s",
			hash, note, logger.SanitizeLogValue(err.Error()))
		return "", note, true
	}
	// The route imports exactly one file, so an exact match against one entry
	// of the torrent's file list is the strong check. Compare cleaned
	// absolute paths, never raw strings.
	want, absErr := filepath.Abs(contentPath)
	if absErr != nil {
		httputil.RespondWithBadRequest(c, "content_path is not a valid path")
		return "", "", false
	}
	for _, f := range detail.Files {
		if abs, aerr := filepath.Abs(detail.AbsPath(f)); aerr == nil && abs == want {
			return hash, "", true
		}
	}
	httputil.RespondWithBadRequest(c, "content_path is not a file of that torrent")
	return "", "", false
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
