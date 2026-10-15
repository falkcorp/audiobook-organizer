// file: internal/server/maintenance_fixups.go
// version: 2.24.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-10-14

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/gin-gonic/gin"
)

// The maintenance fixups' database surface, grouped by what each part is for.
// maintenanceStore was seven database.* embeds — 131 methods — until 2026-08-18.
type maintenanceBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	// ModifyBook, not UpdateBook: the series merge and normalize passes write
	// one column each and must not revert a concurrent writer's columns
	// (database.BookMutator.ModifyBook).
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

type maintenanceSeriesStore interface {
	GetAllSeries() ([]database.Series, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	// No per-series AllVersions getter, on purpose. Every AllVersions reader
	// behind this interface (the normalize positions loop and the merge pass)
	// loads membership once via database.SeriesMembershipAllVersions
	// (SERIES-MERGE-PERSERIES-SCAN-COST, SERIES-MEMBERSHIP-RESIDUAL-LOOPS), so
	// a per-series read in those loops is a compile error, not a full "book:"
	// scan per series.
	UpdateSeriesName(id int, name string) error
	DeleteSeries(id int) error
	// The series-normalize rename pass journals a series_rename row per
	// rename so the revert can undo it.
	CreateOperationChange(change *database.OperationChange) error
}

type maintenanceStore interface {
	maintenanceBookStore
	maintenanceSeriesStore
	// Needed by writeStrippedSeriesPositions: the series-normalize pass
	// rewrites series_name/series_position, both user-lockable, so its write
	// goes through database.ApplyRespectingLocks like every other metadata
	// writer. That guard needs to read the field-lock state.
	database.MetadataFieldStateReader
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
	// Protected: the fix was not written because the file is protected.
	Protected bool   `json:"protected,omitempty"`
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
			"opID", logger.SanitizeLogValue(opID), "jobID", jobID, "error", err)
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
			"opID", logger.SanitizeLogValue(opID), "jobID", jobID, "error", err)
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
		// Protected: the write guard refused the file. Its category still says
		// what is wrong with the tag; it is also counted as skipped_protected
		// and is not listed as a problem, because it is never fixed here.
		// "skipped_protected" as a category is what results written before
		// 2026-09-14 carry.
		if r.Protected {
			counts["skipped_protected"]++
		}
		if r.Category != "ok" && r.Category != "missing" && r.Category != "skipped_protected" && !r.Protected {
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
	// RespondWithOK supplies the {"data": ...} envelope itself. Wrapping stats
	// in a second `struct{ Data any }` here answered {"data":{"data":{...}}},
	// and the web client unwraps exactly one level -- so every field read as
	// undefined and the Maintenance tab crashed on total_book_files. Hand the
	// payload over bare, the way handleGetBookMetadataHashStats below does.
	httputil.RespondWithOK(c, stats)
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
