// file: internal/server/maintenance_fixups.go
// version: 2.16.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-08-24

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// The maintenance fixups' database surface, grouped by what each part is for.
// maintenanceStore was seven database.* embeds — 131 methods — until 2026-08-18.
type libraryCounters interface {
	CountAuthors() (int, error)
	CountFiles() (int, error)
	CountPrimaryBooks() (int, error)
	CountSeries() (int, error)
}

type maintenanceBookStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	DeleteBookFilesForBook(bookID string) error
}

type maintenanceSeriesStore interface {
	GetAllSeries() ([]database.Series, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	// Display may filter; anything that WRITES must not. A merge repoints the
	// rows it is handed and then deletes the series, so a row the Core listing
	// getter hides is a row left pointing at a series that no longer exists.
	GetBooksBySeriesIDAllVersions(seriesID int) ([]database.BookCore, error)
	UpdateSeriesName(id int, name string) error
	DeleteSeries(id int) error
}

// prefixWiper is the pair of raw key-space operations the /maintenance/wipe
// handlers need. Both are *database.PebbleStore-only capabilities that
// database.Store does NOT declare -- compile-probed 2026-08-19 with
// `var _ interface{...} = (database.Store)(nil)`, built with -gcflags=-e --
// so a bare `store.(*database.PebbleStore)` assertion fails against any
// decorator and these handlers silently take their fallback branch.
//
// Both halves have the SAME reachability, so this composite carries no hidden
// weak member; contrast dedup's lshCandidateStore, where one method is on
// database.Store and one is not and the pair therefore inherits the worse of
// the two.
//
// Named rather than resolved with database.AsPebbleStore so nothing in this
// package depends on the concrete type by name. That is what a later split of
// PebbleStore needs first -- see
// docs/plans/2026-08-19-split-the-pebblestore-surface.md.
type prefixWiper interface {
	WipeByPrefixes(prefixes []string) (int, error)
	CountByPrefix(prefix string) (int, error)
}

// resolvePrefixWiper walks the decorator chain for prefixWiper, returning nil
// for a genuinely non-Pebble backend (SQLite, a test double) so each caller
// keeps its existing fallback.
//
// A named function rather than an inline assertion for the same reason
// resolveWarmupWaiter and resolveLSHCandidateStore are: a guard that cannot
// reach the production call site does not guard it, and a test can only
// exercise what it can name.
func resolvePrefixWiper(s any) prefixWiper {
	if c, ok := database.AsCapability[prefixWiper](s); ok {
		return c
	}
	return nil
}

type maintenanceStore interface {
	libraryCounters
	maintenanceBookStore
	maintenanceSeriesStore
	// Needed by writeStrippedSeriesPositions: the series-normalize pass
	// rewrites series_name/series_position, both user-lockable, so its write
	// goes through database.ApplyRespectingLocks like every other metadata
	// writer. That guard needs to read the field-lock state.
	database.MetadataFieldStateReader
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

	store := s.storeForWiring()
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
			books, err := store.GetAllBooksCore(batchSize, offset)
			if err != nil {
				slog.Warn("wipe files GetAllBooksCore failed", "error", err)
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

	// Targets whose wipe returned an error. Every branch below records its
	// count regardless, because a failed wipe still deletes whatever it got
	// through before failing and the operator needs that number. But a count
	// alone cannot distinguish "deleted 500 of 500" from "deleted 500 of 750
	// and stopped" -- so the ones that did not finish are named explicitly.
	//
	// This became load-bearing when WipeAllActivity gained cancellation: before
	// that, no target could return a partial count with an error, so reporting
	// every result as complete was merely imprecise rather than wrong. It is now
	// reachable on any abandoned request. The other six targets are given the
	// same treatment because they have always had the same shape -- an errored
	// wipe reported as a finished one -- and splitting the behaviour would leave
	// the response meaning two different things depending on the target.
	var incomplete []string

	// ── book_files (db rows only) ──────────────────────────────────────────
	if targetSet["book_files"] {
		n, err := wipeBookFiles(store, dryRun)
		if err != nil {
			slog.Warn("wipe book_files failed", "error", err)
			incomplete = append(incomplete, "book_files")
		}
		results["book_files"] = n
	}

	// ── segments ──────────────────────────────────────────────────────────
	if targetSet["segments"] {
		n, err := wipeSegments(store, dryRun)
		if err != nil {
			slog.Warn("wipe segments failed", "error", err)
			incomplete = append(incomplete, "segments")
		}
		results["segments"] = n
	}

	// ── books ──────────────────────────────────────────────────────────────
	if targetSet["books"] {
		n, err := wipeBooks(store, dryRun)
		if err != nil {
			slog.Warn("wipe books failed", "error", err)
			incomplete = append(incomplete, "books")
		}
		results["books"] = n
	}

	// ── authors ────────────────────────────────────────────────────────────
	if targetSet["authors"] {
		n, err := wipeAuthors(store, dryRun)
		if err != nil {
			slog.Warn("wipe authors failed", "error", err)
			incomplete = append(incomplete, "authors")
		}
		results["authors"] = n
	}

	// ── series ─────────────────────────────────────────────────────────────
	if targetSet["series"] {
		n, err := wipeSeries(store, dryRun)
		if err != nil {
			slog.Warn("wipe series failed", "error", err)
			incomplete = append(incomplete, "series")
		}
		results["series"] = n
	}

	// ── external_ids ───────────────────────────────────────────────────────
	if targetSet["external_ids"] {
		n, err := wipeExternalIDs(store, dryRun)
		if err != nil {
			slog.Warn("wipe external_ids failed", "error", err)
			incomplete = append(incomplete, "external_ids")
		}
		results["external_ids"] = n
	}

	// ── activity ──────────────────────────────────────────────────────────
	if targetSet["activity"] {
		if s.activityService != nil {
			n, err := wipeActivity(c.Request.Context(), s.activityService, dryRun)
			if err != nil {
				slog.Warn("wipe activity failed", "error", err)
				incomplete = append(incomplete, "activity")
			}
			results["activity"] = n
		} else {
			slog.Info("wipe activity activityService not initialized, skipping")
		}
	}

	if len(incomplete) > 0 {
		slog.Warn("wipe stopped early", "dry_run", dryRun, "targets", req.Targets,
			"results", results, "incomplete", incomplete)
	} else {
		slog.Info("wipe complete", "dry_run", dryRun, "targets", req.Targets, "results", results)
	}
	httputil.RespondWithOK(c, struct {
		DryRun     bool             `json:"dry_run"`
		Results    map[string]int64 `json:"results"`
		Incomplete []string         `json:"incomplete,omitempty"`
	}{DryRun: dryRun, Results: results, Incomplete: incomplete})
}

// dryRunLabel returns a label for logging.
func dryRunLabel(dryRun bool) string {
	if dryRun {
		return "[dry-run] would delete"
	}
	return "deleting"
}

// wipeBookFiles deletes all book_file rows using the appropriate store backend.
// The wipe* helpers below take their store from Server.Store() at REQUEST time,
// which in production is the Bleve search-index decorator, not the bare
// *PebbleStore. They must therefore resolve THROUGH the decorator chain, which
// is what resolvePrefixWiper does — a bare `store.(*database.PebbleStore)`
// assertion fails against the decorator and silently sends each of these down
// its non-Pebble path: an "unsupported store type" error for
// wipeSegments/wipeBooks/wipeAuthors/wipeSeries/wipeExternalIDs, and for
// wipeBookFiles a slower interface loop that self-reports an approximate count
// and misses the secondary-index prefixes.
func wipeBookFiles(store maintenanceStore, dryRun bool) (int64, error) {
	if dryRun {
		// Count only.
		n, err := store.CountFiles()
		return int64(n), err
	}
	if s := resolvePrefixWiper(store); s != nil {
		n, err := s.WipeByPrefixes([]string{"book_file:"})
		return int64(n), err
	}
	// Fallback: iterate all books and delete via interface.
	var count int64
	offset := 0
	for {
		books, err := store.GetAllBooksCore(500, offset)
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
	if s := resolvePrefixWiper(store); s != nil {
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
	if s := resolvePrefixWiper(store); s != nil {
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
	if s := resolvePrefixWiper(store); s != nil {
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
	if s := resolvePrefixWiper(store); s != nil {
		n, err := s.WipeByPrefixes([]string{"series:"})
		return int64(n), err
	}
	return 0, fmt.Errorf("wipeSeries: unsupported store type %T", store)
}

// wipeExternalIDs deletes all external_id_map rows using the appropriate store backend.
func wipeExternalIDs(store maintenanceStore, dryRun bool) (int64, error) {
	if s := resolvePrefixWiper(store); s != nil {
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
//
// NOTE (pre-existing, not introduced here): the dry-run count comes from
// Query's total, which the bounded scan added in 0adf6e97 made a LOWER BOUND.
// With Limit:1 the walk stops at offset+limit+1 == 2 matches, so the reported
// dry-run count now saturates at 2 rather than reporting the real row count.
// Left alone deliberately — fixing it means either a dedicated count path or a
// different filter, which is out of scope for the cancellation work.
func wipeActivity(ctx context.Context, svc *activity.Service, dryRun bool) (int64, error) {
	if dryRun {
		entries, total, err := svc.Query(ctx, database.ActivityFilter{Limit: 1})
		if err != nil {
			return 0, err
		}
		_ = entries
		return int64(total), nil
	}
	return svc.Store().WipeAllActivity(ctx)
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

// maintenanceResultOp is the run metadata both maintenance result routes render
// alongside the per-item results they decode.
//
// It is a keyspace-neutral projection on purpose. The two routes below are the
// only readers left that care which operations table a maintenance run was
// recorded in, and after the v1 minter was retired a single job id spans BOTH:
// runs started before the retirement have a v1 row, runs started after have a v2
// row, and the results themselves are keyed by an operation id STRING in a table
// with no foreign key to either. Projecting both shapes onto one struct is what
// lets the rendering below stay identical for a run from either era.
type maintenanceResultOp struct {
	Status   string
	Progress int
	Total    int
}

// errMaintenanceOpWrongJob distinguishes "this id names some other operation"
// from "this id names nothing", so the routes can answer 400 and 404
// respectively instead of collapsing both into one misleading status.
var errMaintenanceOpWrongJob = errors.New("operation belongs to a different job")

// lookupMaintenanceResultOp resolves opID to the run that produced the results
// stored under it, looking in BOTH operations keyspaces.
//
// V2 IS TRIED FIRST because it is the only keyspace new runs land in:
// runMaintenanceJob mints a v2 row and nothing else, and returns that row's id
// to the caller, so every id an operator can have obtained since the retirement
// resolves on the first lookup.
//
// THE V1 FALLBACK IS NOT VESTIGIAL. These are operator-facing curl surfaces with
// no frontend consumer, reached by pasting an operation id from a past run, and
// the v1 rows for those runs are neither migrated nor deleted by the retirement
// — they simply stop being created. Dropping the fallback would 404 every run
// recorded before the deploy while its results sat intact in the results table.
//
// legacyTypes are the v1 Type values that identify this job. There is more than
// one because the v1 dispatcher renamed them once already: the pre-ASYNC-CLEAN-1
// name, and the "maintenance:<job>" name the job dispatcher wrote afterwards.
func (s *Server) lookupMaintenanceResultOp(opID, jobID string, legacyTypes ...string) (maintenanceResultOp, error) {
	store := s.Ops()
	if store == nil {
		return maintenanceResultOp{}, fmt.Errorf("database not initialized")
	}

	// Both stores distinguish a miss from a failure the same way: (nil, nil)
	// means "no such row", (nil, err) means the lookup itself broke. Check the
	// error FIRST and propagate it. Folding the two together -- the shape this
	// code had on its first pass -- answers a Pebble failure with 404 "operation
	// not found", which reads to an operator as "your id is wrong" and sends
	// them hunting for a bad id instead of a sick database.
	row, err := store.GetOperationV2(opID)
	if err != nil {
		slog.Error("maintenance result lookup failed reading the v2 operations store",
			"opID", opID, "jobID", jobID, "error", err)
		return maintenanceResultOp{}, fmt.Errorf("reading v2 operation %s: %w", opID, err)
	}
	if row != nil {
		if row.DefID != maintenanceOpID(jobID) {
			return maintenanceResultOp{}, errMaintenanceOpWrongJob
		}
		return maintenanceResultOp{
			Status:   row.Status,
			Progress: row.ProgressCurrent,
			Total:    row.ProgressTotal,
		}, nil
	}

	op, err := store.GetOperationByID(opID)
	if err != nil {
		slog.Error("maintenance result lookup failed reading the v1 operations store",
			"opID", opID, "jobID", jobID, "error", err)
		return maintenanceResultOp{}, fmt.Errorf("reading v1 operation %s: %w", opID, err)
	}
	if op == nil {
		return maintenanceResultOp{}, os.ErrNotExist
	}
	if !slices.Contains(legacyTypes, op.Type) {
		return maintenanceResultOp{}, errMaintenanceOpWrongJob
	}
	return maintenanceResultOp{Status: op.Status, Progress: op.Progress, Total: op.Total}, nil
}

func (s *Server) handleGetComposerScanResults(c *gin.Context) {
	opID := c.Param("id")
	if opID == "" {
		httputil.RespondWithBadRequest(c, "operation id required")
		return
	}

	store := s.Ops()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	op, err := s.lookupMaintenanceResultOp(opID, "scan-composer-tags",
		"composer_tag_scan", "maintenance:scan-composer-tags")
	switch {
	case errors.Is(err, errMaintenanceOpWrongJob):
		httputil.RespondWithBadRequest(c, "not a composer_tag_scan operation")
		return
	case errors.Is(err, os.ErrNotExist):
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	case err != nil:
		// A store failure is not a missing id. Answering 404 here would tell the
		// operator their operation id was wrong when the database is the thing
		// that broke.
		httputil.InternalError(c, "failed to look up operation", err)
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
	store := s.Ops()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	op, err := s.lookupMaintenanceResultOp(opID, "repair-missing-files",
		"missing-file-repair", "maintenance:repair-missing-files")
	switch {
	case errors.Is(err, errMaintenanceOpWrongJob):
		httputil.RespondWithBadRequest(c, "not a missing-file-repair operation")
		return
	case errors.Is(err, os.ErrNotExist):
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	case err != nil:
		// A store failure is not a missing id. Answering 404 here would tell the
		// operator their operation id was wrong when the database is the thing
		// that broke.
		httputil.InternalError(c, "failed to look up operation", err)
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
	store := s.Ops()
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
	store := s.Ops()
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
	store := s.Ops()
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
