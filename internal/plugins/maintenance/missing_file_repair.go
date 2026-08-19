// file: internal/plugins/maintenance/missing_file_repair.go
// version: 2.0.0
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

	// MaxFlagged caps how many rows a single run may report. 0 uses the default
	// below. It exists so a mistaken PathPrefix, or a mount that vanished
	// between the audit and this run, cannot delete the library in one pass.
	MaxFlagged int `json:"max_flagged"`

	// MaxDeletes is accepted and ignored so an old caller's params still parse;
	// it never meant anything after deletion was removed.
	MaxDeletes int `json:"max_deletes"`
}

// missingFileRepairDefaultMax is the ceiling applied when MaxFlagged is unset.
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

	RowsFlagged []string // book_file IDs, only from repairable books
	SamplePaths []string
	FullyBroken []string // book IDs, for you to look at by hand
	Unreadable  []string // paths that could not be stat'd

	CappedAt int // >0 when MaxFlagged truncated the plan
}

func (p repairPlan) summary() string {
	return fmt.Sprintf(
		"books=%d repairable=%d fully-broken(skipped)=%d unreadable(skipped)=%d intact=%d rows_flagged=%d",
		p.BooksExamined, p.BooksRepairable, p.BooksFullyBroken,
		p.BooksSkippedUnreadable, p.BooksIntact, len(p.RowsFlagged))
}

func (p *Plugin) missingFileRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.missing-file-repair",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Missing file report",
		Description: "REPORT ONLY — this operation never deletes anything. It stats every " +
			"book_file row, groups the dead ones per book, and reports what needs a human " +
			"decision. Deletion was removed on 2026-08-19 after the full-population audit " +
			"found that rows this op classified as safe to prune are the only pointer to " +
			"files that exist on disk under a different name. Passing {\"apply\": true} is " +
			"an error, not a no-op.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.missing-file-repair",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		// 🔴 CapLibraryWrite REMOVED 2026-08-19. This op reports; it does not write.
		// Declaring the capability it no longer needs would leave the door open for
		// a future edit to walk back through it without anyone re-deciding.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runMissingFileRepair,
	}
}

func (p *Plugin) runMissingFileRepair(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params missingFileRepairParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	// 🔴 Deletion was REMOVED from this op on 2026-08-19. Rejecting apply loudly
	// rather than ignoring it: a parameter that silently stops doing what its name
	// says is the exact shape of silent failure this codebase spends effort
	// removing, and any caller still passing it is acting on a stale belief about
	// what this op does.
	if params.Apply {
		return fmt.Errorf(
			"{\"apply\": true} is no longer supported: this operation never deletes. " +
				"The full-population audit (docs/audits/2026-08-17-missing-file-audit-full-population.md) " +
				"found that rows this op classified as safe to prune are the only pointer to files that " +
				"exist on disk under a different name -- deleting them orphans the bytes. " +
				"Run it without params for the report, and see the classify pass on " +
				"maintenance.missing-file-audit for which rows are recoverable")
	}

	plan, err := planMissingFileRepair(ctx, store, params, reporter)
	if err != nil {
		return err
	}

	log := reporter.Logger()
	log.Info("missing-file-report plan", "summary", plan.summary(),
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
		log.Warn("missing-file-report: report truncated by max_flagged",
			"cap", plan.CappedAt, "run_again_to_continue", true)
	}

	if len(plan.RowsFlagged) > 0 {
		log.Warn("missing-file-report: rows are dead and their book still has a surviving file — "+
			"THESE NEED YOUR DECISION, they are not deleted automatically",
			"rows_flagged", len(plan.RowsFlagged),
			"books_affected", plan.BooksRepairable,
			"sample_paths", plan.SamplePaths)
	}

	log.Info("missing-file-report complete — REPORT ONLY, nothing was modified",
		"rows_flagged_for_review", len(plan.RowsFlagged),
		"books_fully_broken_needing_decision", plan.BooksFullyBroken,
		"paths_unreadable", len(plan.Unreadable))
	return nil
}

// planMissingFileRepair stats every candidate row and decides, per BOOK, what is
// safe to prune. Returned as a value so the decisions can be asserted directly in
// tests rather than inferred from side effects.
func planMissingFileRepair(ctx context.Context, store bookFileCoreScanner, params missingFileRepairParams, reporter sdk.Reporter) (repairPlan, error) {
	log := reporter.Logger()
	maxFlagged := params.MaxFlagged
	if maxFlagged <= 0 {
		maxFlagged = params.MaxDeletes
	}
	if maxFlagged <= 0 {
		maxFlagged = missingFileRepairDefaultMax
	}
	// No "apply" field: runMissingFileRepair rejects apply before reaching here,
	// so logging it would only ever print false and imply the mode still exists.
	log.Info("missing-file-repair start",
		"path_prefix", params.PathPrefix, "max_flagged", maxFlagged)

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
				if len(plan.RowsFlagged) >= maxFlagged {
					plan.CappedAt = maxFlagged
					break
				}
				plan.RowsFlagged = append(plan.RowsFlagged, rowID)
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

// 🔴 applyMissingFileRepair WAS HERE AND HAS BEEN DELETED (2026-08-19).
//
// It batched plan.RowsFlagged into DeleteBookFilesByIDs. It is removed rather
// than left unwired, because unwired delete code is one edit away from being
// wired again by someone who has not read the audit. The whole point of this
// change is that these rows must never be deleted automatically -- they are
// surfaced for a human decision instead.
//
// If a repair is ever built, it must REPOINT (update file_path to the file that
// exists) and not delete. See
// docs/audits/2026-08-17-missing-file-audit-full-population.md.
