// file: internal/plugins/maintenance/missing_file_repair.go
// version: 1.1.0
// guid: 50b5022c-9d86-467d-991e-2be9cddf4847
// last-edited: 2026-08-17

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// missingFileRepairDeleteBatch bounds one DeleteBookFilesByIDs call. The store
// doc records that per-row DeleteBookFile pays a fixed cost of ~1.35s/row, so
// batching is not a micro-optimisation here — at 552 rows the difference is
// twelve minutes versus a moment.
const missingFileRepairDeleteBatch = 200

// missingFileRepairSampleLimit bounds how many example paths and book IDs are
// carried in the report.
const missingFileRepairSampleLimit = 60

// missingFileRepairParams are the JSON parameters accepted by the op.
type missingFileRepairParams struct {
	// Apply must be set explicitly to delete anything. The zero value is a DRY
	// RUN, so triggering this op with no params reports what it would do and
	// changes nothing. Defaulting the destructive direction to "off" is the
	// point: the safe outcome must be what you get by forgetting a flag.
	Apply bool `json:"apply"`

	// PathPrefix restricts the sweep to rows whose FilePath begins with it.
	PathPrefix string `json:"path_prefix"`

	// MaxDeletes caps how many rows a single run may delete. 0 uses the default
	// below. It exists so a mistaken PathPrefix, or a mount that vanished
	// between the audit and this run, cannot delete the library in one pass.
	MaxDeletes int `json:"max_deletes"`
}

// missingFileRepairDefaultMax is the ceiling applied when MaxDeletes is unset.
// The 2026-08-17 sample projected ~552 dead rows per 120 books; this is
// deliberately generous enough for a real repair and far below "everything".
const missingFileRepairDefaultMax = 20000

// repairPlan is what the sweep decides BEFORE anything is deleted. It is
// computed identically in dry-run and apply mode — apply simply also executes
// it — so what you review is exactly what runs.
type repairPlan struct {
	BooksExamined          int
	BooksRepairable        int // ≥1 dead row AND ≥1 surviving row → safe to prune
	BooksFullyBroken       int // every row dead → SKIPPED, never touched
	BooksSkippedUnreadable int // ≥1 row we could not stat → SKIPPED
	BooksIntact            int

	RowsToDelete []string // book_file IDs, only from repairable books
	SamplePaths  []string
	FullyBroken  []string // book IDs, for you to look at by hand
	Unreadable   []string // paths that could not be stat'd

	CappedAt int // >0 when MaxDeletes truncated the plan
}

func (p repairPlan) summary() string {
	return fmt.Sprintf(
		"books=%d repairable=%d fully-broken(skipped)=%d unreadable(skipped)=%d intact=%d rows_to_delete=%d",
		p.BooksExamined, p.BooksRepairable, p.BooksFullyBroken,
		p.BooksSkippedUnreadable, p.BooksIntact, len(p.RowsToDelete))
}

func (p *Plugin) missingFileRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.missing-file-repair",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Missing file repair",
		Description: "Deletes book_file rows whose bytes are gone, but ONLY for books that still " +
			"have at least one surviving file. Books whose every row is dead are skipped and " +
			"reported, never emptied — deleting their rows would turn a wrong index into a lost " +
			"book. Books with any un-stat-able path are skipped too. DRY RUN by default: pass " +
			"{\"apply\": true} to actually delete.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.missing-file-repair",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runMissingFileRepair,
	}
}

func (p *Plugin) runMissingFileRepair(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params missingFileRepairParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	plan, err := planMissingFileRepair(ctx, store, params, reporter)
	if err != nil {
		return err
	}

	log := reporter.Logger()
	mode := "DRY RUN"
	if params.Apply {
		mode = "APPLY"
	}
	log.Info("missing-file-repair plan", "mode", mode, "summary", plan.summary(),
		"sample_paths", plan.SamplePaths)
	if len(plan.FullyBroken) > 0 {
		log.Warn("missing-file-repair: books with NO surviving file were skipped — "+
			"these need a human decision, not an automatic delete",
			"count", plan.BooksFullyBroken, "book_ids", plan.FullyBroken)
	}
	if len(plan.Unreadable) > 0 {
		log.Warn("missing-file-repair: paths could not be stat'd; their books were skipped entirely",
			"count", len(plan.Unreadable), "sample", plan.Unreadable)
	}
	if plan.CappedAt > 0 {
		log.Warn("missing-file-repair: plan truncated by max_deletes",
			"cap", plan.CappedAt, "run_again_to_continue", true)
	}

	if !params.Apply {
		log.Info("missing-file-repair: DRY RUN complete — nothing was deleted. "+
			"Re-run with {\"apply\": true} to execute this exact plan.",
			"rows_that_would_be_deleted", len(plan.RowsToDelete))
		return nil
	}

	deleted, err := applyMissingFileRepair(ctx, store, plan, reporter)
	log.Info("missing-file-repair complete", "mode", mode, "rows_deleted", deleted,
		"books_skipped_fully_broken", plan.BooksFullyBroken)
	return err
}

// planMissingFileRepair stats every candidate row and decides, per BOOK, what is
// safe to prune. Returned as a value so the decisions can be asserted directly in
// tests rather than inferred from side effects.
func planMissingFileRepair(ctx context.Context, store bookFileCoreScanner, params missingFileRepairParams, reporter sdk.Reporter) (repairPlan, error) {
	log := reporter.Logger()
	maxDeletes := params.MaxDeletes
	if maxDeletes <= 0 {
		maxDeletes = missingFileRepairDefaultMax
	}
	log.Info("missing-file-repair start", "apply", params.Apply,
		"path_prefix", params.PathPrefix, "max_deletes", maxDeletes)

	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return repairPlan{}, fmt.Errorf("load book files: %w", err)
	}

	items := make([]missingFileItem, 0, len(files))
	for i := range files {
		path := strings.TrimSpace(files[i].FilePath)
		if path == "" {
			continue
		}
		if params.PathPrefix != "" && !strings.HasPrefix(path, params.PathPrefix) {
			continue
		}
		items = append(items, missingFileItem{idx: len(items), file: files[i]})
	}

	results := make([]fileExistence, len(items))
	var missing, present, unreadable atomic.Int64

	prog := sdk.NewProgress(reporter, len(items))
	prog.Start(fmt.Sprintf("Checking %d book_file path(s)…", len(items)))

	err = registry.RunItems(ctx, reporter, items, func(_ context.Context, it missingFileItem) error {
		switch _, serr := os.Stat(it.file.FilePath); {
		case serr == nil:
			results[it.idx] = filePresent
			present.Add(1)
		case os.IsNotExist(serr):
			results[it.idx] = fileMissing
			missing.Add(1)
		default:
			results[it.idx] = fileUnreadable
			unreadable.Add(1)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Checked %d/%d paths (missing=%d)", i+1, t, missing.Load())
		},
	})
	if err != nil {
		return repairPlan{}, fmt.Errorf("stat sweep: %w", err)
	}

	// Group by book. The safety decision is per BOOK, not per row: whether a
	// dead row may be deleted depends entirely on whether its book keeps
	// something else.
	type bookState struct {
		deadIDs    []string
		deadPaths  []string
		present    int
		unreadable int
	}
	books := map[string]*bookState{}
	order := make([]string, 0, len(items))
	for i, it := range items {
		st := books[it.file.BookID]
		if st == nil {
			st = &bookState{}
			books[it.file.BookID] = st
			order = append(order, it.file.BookID)
		}
		switch results[i] {
		case filePresent:
			st.present++
		case fileMissing:
			st.deadIDs = append(st.deadIDs, it.file.ID)
			st.deadPaths = append(st.deadPaths, it.file.FilePath)
		case fileUnreadable:
			st.unreadable++
		}
	}

	plan := repairPlan{BooksExamined: len(books)}
	for _, id := range order {
		st := books[id]
		switch {
		case st.unreadable > 0:
			// "I could not tell" is not "it is gone". One unmounted share would
			// otherwise present an entire tree as deletable.
			plan.BooksSkippedUnreadable++
		case len(st.deadIDs) == 0:
			plan.BooksIntact++
		case st.present == 0:
			// Every row dead. Deleting them all leaves a book with no files at
			// all — a wrong index becomes a lost book. Skipped by design.
			plan.BooksFullyBroken++
			if len(plan.FullyBroken) < missingFileRepairSampleLimit {
				plan.FullyBroken = append(plan.FullyBroken, id)
			}
		default:
			plan.BooksRepairable++
			for i, rowID := range st.deadIDs {
				if len(plan.RowsToDelete) >= maxDeletes {
					plan.CappedAt = maxDeletes
					break
				}
				plan.RowsToDelete = append(plan.RowsToDelete, rowID)
				if len(plan.SamplePaths) < missingFileRepairSampleLimit {
					plan.SamplePaths = append(plan.SamplePaths, st.deadPaths[i])
				}
			}
		}
	}

	// Collect a sample of unreadable paths for the report.
	for i, it := range items {
		if results[i] == fileUnreadable && len(plan.Unreadable) < missingFileRepairSampleLimit {
			plan.Unreadable = append(plan.Unreadable, it.file.FilePath)
		}
	}
	sort.Strings(plan.FullyBroken)

	return plan, nil
}

// applyMissingFileRepair executes a plan. Every deleted path is logged before the
// delete, so the change is reconstructible from the operation log rather than
// merely gone.
func applyMissingFileRepair(ctx context.Context, store bookFileBulkDeleter, plan repairPlan, reporter sdk.Reporter) (int, error) {
	log := reporter.Logger()
	deleted := 0
	for off := 0; off < len(plan.RowsToDelete); off += missingFileRepairDeleteBatch {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		end := off + missingFileRepairDeleteBatch
		if end > len(plan.RowsToDelete) {
			end = len(plan.RowsToDelete)
		}
		batch := plan.RowsToDelete[off:end]
		log.Info("missing-file-repair: deleting batch", "from", off, "to", end, "ids", batch)
		if err := store.DeleteBookFilesByIDs(batch); err != nil {
			return deleted, fmt.Errorf("delete rows [%d:%d]: %w", off, end, err)
		}
		deleted += len(batch)
		_ = reporter.UpdateProgress(deleted, len(plan.RowsToDelete),
			fmt.Sprintf("Deleted %d/%d dead rows", deleted, len(plan.RowsToDelete)))
	}
	return deleted, nil
}
