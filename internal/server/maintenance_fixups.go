// file: internal/server/maintenance_fixups.go
// version: 2.6.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-06-23

package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// maintenanceStore is the narrow slice of database.Store that the
// wipe-helper free functions accept.
type maintenanceStore interface {
	database.BookStore
	database.AuthorStore
	database.SeriesStore
	database.BookFileStore
	database.UserTagStore
	database.ExternalIDStore
	database.StatsStore
}

func (s *Server) handleWipe(c *gin.Context) {
	var req struct {
		Targets []string `json:"targets"`
		Confirm string   `json:"confirm"`
		DryRun  bool     `json:"dry_run"`
	}
	// Default dry_run to true before binding.
	req.DryRun = true

	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if req.Confirm != "WIPE" {
		httputil.RespondWithBadRequest(c, `must include "confirm": "WIPE" in the request body`)
		return
	}

	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// Expand "all" to every individual target.
	targetSet := make(map[string]bool, len(req.Targets))
	for _, t := range req.Targets {
		targetSet[t] = true
	}
	if targetSet["all"] {
		for _, t := range []string{
			"books", "book_files", "segments", "files",
			"organized_folders", "activity", "authors", "series", "external_ids",
		} {
			targetSet[t] = true
		}
	}

	results := make(map[string]int64)
	dryRun := req.DryRun

	// ── organized_folders ──────────────────────────────────────────────────
	if targetSet["organized_folders"] {
		rootDir := config.AppConfig.RootDir
		keep := map[string]bool{
			".covers":           true,
			".itunes-writeback": true,
			"openlibrary-dumps": true,
		}
		entries, err := os.ReadDir(rootDir)
		if err != nil {
			slog.Warn("wipe can't read root dir", "path", rootDir, "error", err)
		} else {
			var count int64
			for _, e := range entries {
				// Skip hidden dirs (starting with ".") that are not in the keeplist,
				// but only delete non-hidden dirs or explicitly non-kept hidden dirs.
				if strings.HasPrefix(e.Name(), ".") && !keep[e.Name()] {
					continue // skip unknown hidden dirs
				}
				if keep[e.Name()] {
					continue
				}
				fullPath := filepath.Join(rootDir, e.Name())
				slog.Info("wipe organized_folders", "action", dryRunLabel(dryRun), "path", fullPath)
				if !dryRun {
					if err := os.RemoveAll(fullPath); err != nil {
						slog.Warn("wipe RemoveAll failed", "path", fullPath, "error", err)
					}
				}
				count++
			}
			results["organized_folders"] = count
		}
	}

	// ── files (disk + db rows) ─────────────────────────────────────────────
	// "files" implies "book_files" as well — collect file paths first, then delete rows.
	if targetSet["files"] {
		rootDir := config.AppConfig.RootDir
		var count int64
		offset := 0
		batchSize := 500
		for {
			books, err := store.GetAllBooks(batchSize, offset)
			if err != nil {
				slog.Warn("wipe files GetAllBooks failed", "error", err)
				break
			}
			for _, book := range books {
				files, ferr := store.GetBookFiles(book.ID)
				if ferr != nil {
					slog.Warn("wipe files GetBookFiles failed", "book_id", book.ID, "error", ferr)
					continue
				}
				for _, bf := range files {
					if bf.FilePath == "" {
						continue
					}
					// Only remove files inside the organizer root dir — never iTunes paths.
					if !strings.HasPrefix(filepath.Clean(bf.FilePath), filepath.Clean(rootDir)) {
						continue
					}
					slog.Info("wipe files", "action", dryRunLabel(dryRun), "path", bf.FilePath)
					if !dryRun {
						if rerr := os.Remove(bf.FilePath); rerr != nil && !os.IsNotExist(rerr) {
							slog.Warn("wipe os.Remove failed", "path", bf.FilePath, "error", rerr)
						}
					}
					count++
				}
			}
			if len(books) < batchSize {
				break
			}
			offset += batchSize
		}
		results["files"] = count
		// "files" also deletes the book_file rows — mark book_files as well.
		targetSet["book_files"] = true
	}

	// ── book_files (db rows only) ──────────────────────────────────────────
	if targetSet["book_files"] {
		n, err := wipeBookFiles(store, dryRun)
		if err != nil {
			slog.Warn("wipe book_files failed", "error", err)
		}
		results["book_files"] = n
	}

	// ── segments ──────────────────────────────────────────────────────────
	if targetSet["segments"] {
		n, err := wipeSegments(store, dryRun)
		if err != nil {
			slog.Warn("wipe segments failed", "error", err)
		}
		results["segments"] = n
	}

	// ── books ──────────────────────────────────────────────────────────────
	if targetSet["books"] {
		n, err := wipeBooks(store, dryRun)
		if err != nil {
			slog.Warn("wipe books failed", "error", err)
		}
		results["books"] = n
	}

	// ── authors ────────────────────────────────────────────────────────────
	if targetSet["authors"] {
		n, err := wipeAuthors(store, dryRun)
		if err != nil {
			slog.Warn("wipe authors failed", "error", err)
		}
		results["authors"] = n
	}

	// ── series ─────────────────────────────────────────────────────────────
	if targetSet["series"] {
		n, err := wipeSeries(store, dryRun)
		if err != nil {
			slog.Warn("wipe series failed", "error", err)
		}
		results["series"] = n
	}

	// ── external_ids ───────────────────────────────────────────────────────
	if targetSet["external_ids"] {
		n, err := wipeExternalIDs(store, dryRun)
		if err != nil {
			slog.Warn("wipe external_ids failed", "error", err)
		}
		results["external_ids"] = n
	}

	// ── activity ──────────────────────────────────────────────────────────
	if targetSet["activity"] {
		if s.activityService != nil {
			n, err := wipeActivity(s.activityService, dryRun)
			if err != nil {
				slog.Warn("wipe activity failed", "error", err)
			}
			results["activity"] = n
		} else {
			slog.Info("wipe activity activityService not initialized, skipping")
		}
	}

	slog.Info("wipe complete", "dry_run", dryRun, "targets", req.Targets, "results", results)
	httputil.RespondWithOK(c, struct {
		DryRun  bool             `json:"dry_run"`
		Results map[string]int64 `json:"results"`
	}{DryRun: dryRun, Results: results})
}

// dryRunLabel returns a label for logging.
func dryRunLabel(dryRun bool) string {
	if dryRun {
		return "[dry-run] would delete"
	}
	return "deleting"
}

// wipeBookFiles deletes all book_file rows using the appropriate store backend.
func wipeBookFiles(store maintenanceStore, dryRun bool) (int64, error) {
	if dryRun {
		// Count only.
		n, err := store.CountFiles()
		return int64(n), err
	}
	if s, ok := store.(*database.PebbleStore); ok {
		n, err := s.WipeByPrefixes([]string{"book_file:"})
		return int64(n), err
	}
	// Fallback: iterate all books and delete via interface.
	var count int64
	offset := 0
	for {
		books, err := store.GetAllBooks(500, offset)
		if err != nil {
			return count, err
		}
		for _, book := range books {
			if err := store.DeleteBookFilesForBook(book.ID); err != nil {
				slog.Warn("wipeBookFiles DeleteBookFilesForBook failed", "book_id", book.ID, "error", err)
			}
			count++ // approximate
		}
		if len(books) < 500 {
			break
		}
		offset += 500
	}
	return count, nil
}

// wipeSegments deletes all book_segment rows using the appropriate store backend.
func wipeSegments(store maintenanceStore, dryRun bool) (int64, error) {
	if s, ok := store.(*database.PebbleStore); ok {
		// Pebble segments use "bf:" (primary) and "bfs:" (secondary) prefixes.
		if dryRun {
			n, err := s.CountByPrefix("bf:")
			return int64(n), err
		}
		n, err := s.WipeByPrefixes([]string{"bf:", "bfs:"})
		return int64(n), err
	}
	return 0, fmt.Errorf("wipeSegments: unsupported store type %T", store)
}

// wipeBooks deletes all book rows using the appropriate store backend.
func wipeBooks(store maintenanceStore, dryRun bool) (int64, error) {
	if dryRun {
		n, err := store.CountPrimaryBooks()
		return int64(n), err
	}
	if s, ok := store.(*database.PebbleStore); ok {
		// Book keys: "book:" prefix. Include secondary indexes.
		n, err := s.WipeByPrefixes([]string{"book:"})
		return int64(n), err
	}
	return 0, fmt.Errorf("wipeBooks: unsupported store type %T", store)
}

// wipeAuthors deletes all author rows using the appropriate store backend.
func wipeAuthors(store maintenanceStore, dryRun bool) (int64, error) {
	if dryRun {
		n, err := store.CountAuthors()
		return int64(n), err
	}
	if s, ok := store.(*database.PebbleStore); ok {
		n, err := s.WipeByPrefixes([]string{"author:"})
		return int64(n), err
	}
	return 0, fmt.Errorf("wipeAuthors: unsupported store type %T", store)
}

// wipeSeries deletes all series rows using the appropriate store backend.
func wipeSeries(store maintenanceStore, dryRun bool) (int64, error) {
	if dryRun {
		n, err := store.CountSeries()
		return int64(n), err
	}
	if s, ok := store.(*database.PebbleStore); ok {
		n, err := s.WipeByPrefixes([]string{"series:"})
		return int64(n), err
	}
	return 0, fmt.Errorf("wipeSeries: unsupported store type %T", store)
}

// wipeExternalIDs deletes all external_id_map rows using the appropriate store backend.
func wipeExternalIDs(store maintenanceStore, dryRun bool) (int64, error) {
	if s, ok := store.(*database.PebbleStore); ok {
		if dryRun {
			n, err := s.CountByPrefix("ext_id:")
			return int64(n), err
		}
		// "ext_id:" covers both "ext_id:<source>:<id>" and "ext_id:book:<bookID>:<source>:<id>"
		n, err := s.WipeByPrefixes([]string{"ext_id:"})
		return int64(n), err
	}
	return 0, fmt.Errorf("wipeExternalIDs: unsupported store type %T", store)
}

// wipeActivity deletes all activity log entries.
func wipeActivity(svc *activity.Service, dryRun bool) (int64, error) {
	if dryRun {
		entries, total, err := svc.Query(database.ActivityFilter{Limit: 1})
		if err != nil {
			return 0, err
		}
		_ = entries
		return int64(total), nil
	}
	return svc.Store().WipeAllActivity()
}

// composerTagResult describes the COMPOSER field state for one audio file.
type composerTagResult struct {
	BookID    string `json:"book_id"`
	BookTitle string `json:"book_title"`
	FilePath  string `json:"file_path"`
	// Category is one of: "ok", "composer_equals_author", "composer_equals_narrator",
	// "composer_mismatch", "missing_narrator", "read_error".
	Category  string `json:"category"`
	Composer  string `json:"composer_on_disk"`
	Author    string `json:"author,omitempty"`
	Narrator  string `json:"narrator,omitempty"`
	WillWrite string `json:"will_write,omitempty"`
	Applied   bool   `json:"applied,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) handleGetComposerScanResults(c *gin.Context) {
	opID := c.Param("id")
	if opID == "" {
		httputil.RespondWithBadRequest(c, "operation id required")
		return
	}

	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	op, err := store.GetOperationByID(opID)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	}
	// Accept both the legacy "composer_tag_scan" type (pre-ASYNC-CLEAN-1) and
	// the new "maintenance:scan-composer-tags" type created by the job dispatcher.
	if op.Type != "composer_tag_scan" && op.Type != "maintenance:scan-composer-tags" {
		httputil.RespondWithBadRequest(c, "not a composer_tag_scan operation")
		return
	}

	rawResults, err := store.GetOperationResults(opID)
	if err != nil {
		httputil.InternalError(c, "failed to load results", err)
		return
	}

	counts := map[string]int{}
	var problems []composerTagResult
	for _, raw := range rawResults {
		var r composerTagResult
		if err := json.Unmarshal([]byte(raw.ResultJSON), &r); err != nil {
			continue
		}
		counts[r.Category]++
		if r.Category != "ok" && r.Category != "missing" {
			problems = append(problems, r)
		}
	}

	httputil.RespondWithOK(c, struct {
		OperationID string              `json:"operation_id"`
		Status      string              `json:"status"`
		Progress    int                 `json:"progress"`
		Total       int                 `json:"total"`
		ByCategory  map[string]int      `json:"by_category"`
		Problems    int                 `json:"problems"`
		Details     []composerTagResult `json:"details"`
	}{
		OperationID: opID,
		Status:      op.Status,
		Progress:    op.Progress,
		Total:       op.Total,
		ByCategory:  counts,
		Problems:    len(problems),
		Details:     problems,
	})
}

type missingFileRepairResult struct {
	BookID  string `json:"book_id"`
	Title   string `json:"book_title"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path,omitempty"`
	// Method values: "pid", "filename", "truncation", "author_title",
	// "skipped", "unresolved", "ambiguous"
	Method  string `json:"method"`
	Matches int    `json:"matches,omitempty"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handleGetMissingFileRepairResults(c *gin.Context) {
	opID := c.Param("id")
	if opID == "" {
		httputil.RespondWithBadRequest(c, "operation id required")
		return
	}
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	op, err := store.GetOperationByID(opID)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	}
	// Accept both the legacy "missing-file-repair" type (pre-ASYNC-CLEAN-1) and
	// the new "maintenance:repair-missing-files" type created by the job dispatcher.
	if op.Type != "missing-file-repair" && op.Type != "maintenance:repair-missing-files" {
		httputil.RespondWithBadRequest(c, "not a missing-file-repair operation")
		return
	}
	rawResults, err := store.GetOperationResults(opID)
	if err != nil {
		httputil.InternalError(c, "failed to load results", err)
		return
	}

	byMethod := map[string]int{}
	var problems []missingFileRepairResult
	repaired, unresolved, ambiguous, skipped := 0, 0, 0, 0
	for _, raw := range rawResults {
		var r missingFileRepairResult
		if jsonErr := json.Unmarshal([]byte(raw.ResultJSON), &r); jsonErr != nil {
			continue
		}
		byMethod[r.Method]++
		switch r.Method {
		case "unresolved":
			unresolved++
			problems = append(problems, r)
		case "ambiguous":
			ambiguous++
			problems = append(problems, r)
		case "skipped":
			skipped++
		default:
			repaired++
		}
	}

	httputil.RespondWithOK(c, struct {
		OperationID string                    `json:"operation_id"`
		Status      string                    `json:"status"`
		Progress    int                       `json:"progress"`
		Total       int                       `json:"total"`
		ByMethod    map[string]int            `json:"by_method"`
		Repaired    int                       `json:"repaired"`
		Unresolved  int                       `json:"unresolved"`
		Ambiguous   int                       `json:"ambiguous"`
		Skipped     int                       `json:"skipped"`
		Problems    []missingFileRepairResult `json:"problems"`
	}{
		OperationID: opID,
		Status:      op.Status,
		Progress:    op.Progress,
		Total:       op.Total,
		ByMethod:    byMethod,
		Repaired:    repaired,
		Unresolved:  unresolved,
		Ambiguous:   ambiguous,
		Skipped:     skipped,
		Problems:    problems,
	})
}

func (s *Server) handleGetBookFileHashStats(c *gin.Context) {
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	stats, err := store.GetBookFileHashStats()
	if err != nil {
		httputil.InternalError(c, "failed to get book file hash stats", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Data any `json:"data"`
	}{Data: stats})
}

func (s *Server) handleGetBookMetadataHashStats(c *gin.Context) {
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	stats, err := store.GetBookMetadataHashStats()
	if err != nil {
		httputil.InternalError(c, "failed to get book metadata hash stats", err)
		return
	}
	httputil.RespondWithOK(c, stats)
}

// handleGetAcoustIDStats returns AcoustID fingerprint coverage stats.
// GET /api/v1/maintenance/acoustid-stats
func (s *Server) handleGetAcoustIDStats(c *gin.Context) {
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	stats, err := store.GetAcoustIDStats()
	if err != nil {
		httputil.InternalError(c, "failed to get acoustid stats", err)
		return
	}
	httputil.RespondWithOK(c, stats)
}

