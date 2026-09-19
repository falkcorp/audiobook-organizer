// file: internal/plugins/maintenance/orphan_book_files_repoint_plan.go
// version: 1.1.0
// guid: 458004fb-a5f3-4cb8-a25f-741cd3f75b74
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"golang.org/x/sync/errgroup"
)

// maintenance.orphan-book-files-repoint-plan is the DRY-RUN-ONLY census and
// repair plan for book_file rows whose BookID names no book (live or
// soft-deleted). It has no apply mode, by design: it reads, classifies and
// reports, and it never writes a row. Applying any of the planned repoints to
// production is an owner decision (standing rule: never delete book_file rows
// as a repair — repoint them).
//
// The orphans it plans for were created by hard deletes of books that still
// owned file rows — DeleteBook never deleted those rows, and until
// database.ErrBookOwnsFiles it did not refuse either. The soft-delete purge of
// dedup-merge losers is the known producer; archive sweep, reconcile's
// version-group cleanup, batch and user hard delete could all do it too.
//
// Each orphan row is put in exactly one class, strongest evidence first:
//
//   - duplicate_of_owned_row: some OTHER row at the same path belongs to a
//     book that exists (BookFilesAtPath: every row at the path, not the one
//     the single-valued crc32 index happens to hold). The orphan is a second
//     reference to a file some book already owns; repointing it would give
//     that book the same file twice. Planned action: none (owner decides).
//   - duplicate_of_orphan: another orphan row with the same path was already
//     planned onto the same target book. Planned action: none, for the same
//     reason.
//   - repoint_to_path_owner: exactly one live book's FilePath is the file
//     itself or its directory. Planned action: repoint to that book.
//   - repoint_to_merge_survivor: the deleted book left a tombstone naming a
//     MergedIntoBookID, or a version group with exactly one live primary.
//     Planned action: repoint to that book. Note a MergeBooks loser's files
//     are its OWN version, so this puts a second version's files on the
//     survivor — the reason this is a plan for the owner, not an apply.
//   - ambiguous_path_owner: more than one live book sits at the path or its
//     directory. Planned action: none.
//   - unresolved: no evidence. Planned action: none.
//
// Path-owner evidence needs the book_atpath index: LiveBookIDsAtPath scans
// every book row per call until its backfill sentinel exists, which per
// orphan is an outage. When it is unbuilt the path-owner step is skipped for
// every row (PathOwnerSkipped, logged), not silently degraded.
//
// Output: the whole plan (every row, bucketed) is stored as the op's result
// payload — GET /api/v1/operations/<op id>/result — and the log says so. The
// log itself carries only the counts and a sample.
//
// Coverage caveat: the orphan list comes from findOrphanBookFiles, which reads
// book_files through memdb. DeleteBookFromMemDB drops a deleted book's file
// rows from memdb while Pebble keeps them, so orphans created since the last
// restart are invisible until memdb is re-warmed from Pebble. Run it after a
// restart for a full census.

// Orphan repoint classes.
const (
	orphanClassDuplicate      = "duplicate_of_owned_row"
	orphanClassDupOrphan      = "duplicate_of_orphan"
	orphanClassPathOwner      = "repoint_to_path_owner"
	orphanClassMergeSurvivor  = "repoint_to_merge_survivor"
	orphanClassAmbiguousOwner = "ambiguous_path_owner"
	orphanClassUnresolved     = "unresolved"
)

// orphanRepointPlanStore is what the planner reads beyond the orphan scan. All
// of it is on database.Store, so the production indexedStore satisfies it.
type orphanRepointPlanStore interface {
	GetBookByID(id string) (*database.Book, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	BookAtPathIndexBuilt() (bool, error)
	GetBookTombstone(id string) (*database.Book, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// OrphanRepointPlanItem is one orphan row and what the plan would do with it.
type OrphanRepointPlanItem struct {
	BookFileID   string `json:"book_file_id"`
	OrphanBookID string `json:"orphan_book_id"`
	FilePath     string `json:"file_path"`
	Class        string `json:"class"`
	TargetBookID string `json:"target_book_id,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

// OrphanRepointPlan is the census result.
type OrphanRepointPlan struct {
	TotalBookFiles  int            `json:"total_book_files"`
	OwningBooks     int            `json:"owning_books"`
	Orphans         int            `json:"orphans"`
	OrphanBookIDs   int            `json:"orphan_book_ids"`
	ByClass         map[string]int `json:"by_class"`
	PlannedRepoints int            `json:"planned_repoints"`
	// PathOwnerSkipped is set when the book_atpath index is not built, so no
	// row was checked for a live book at its path or directory.
	PathOwnerSkipped string                  `json:"path_owner_skipped,omitempty"`
	Items            []OrphanRepointPlanItem `json:"items"`
}

func (p *Plugin) orphanBookFilesRepointPlanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.orphan-book-files-repoint-plan",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Orphan book_file repoint plan (dry run)",
		Description:     "Read-only: finds book_file rows whose book no longer exists and plans which book each should be repointed to. Never writes.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.orphan-book-files-repoint-plan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runOrphanBookFilesRepointPlan,
	}
}

func (p *Plugin) runOrphanBookFilesRepointPlan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	ops := p.deps.OpsStore()
	if ops == nil {
		return fmt.Errorf("database not initialized")
	}
	planStore, ok := any(ops).(orphanRepointPlanStore)
	if !ok {
		return fmt.Errorf("store %T lacks the lookups the repoint plan needs", ops)
	}
	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Scanning book_files for orphan rows (dry run)...")

	orphans, totalFiles, owners, err := findOrphanBookFiles(ctx, ops)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}
	plan, err := planOrphanRepoints(ctx, planStore, orphans)
	if err != nil {
		return err
	}
	plan.TotalBookFiles = totalFiles
	plan.OwningBooks = owners
	if plan.PathOwnerSkipped != "" {
		_ = reporter.Log(slog.LevelWarn, plan.PathOwnerSkipped)
	}
	// The full plan is the op's result payload, readable afterwards with
	// GET /api/v1/operations/<op id>/result. Fail the op if it cannot be
	// stored: a review artifact that silently is not there is worse than none.
	if err := opsregistry.ReporterSetResult(reporter, plan); err != nil {
		return fmt.Errorf("store the plan as the op result: %w", err)
	}
	where := "GET /api/v1/operations/<this op id>/result"
	if id := opsregistry.ReporterOpID(reporter); id != "" {
		where = "GET /api/v1/operations/" + id + "/result"
	}
	_ = reporter.Log(slog.LevelInfo, "Full plan (every row, bucketed) stored as this op's result: "+where)

	_ = reporter.Log(slog.LevelInfo, "Orphan repoint plan (dry run, nothing written)",
		slog.Int("orphans", plan.Orphans),
		slog.Int("orphan_book_ids", plan.OrphanBookIDs),
		slog.Int("planned_repoints", plan.PlannedRepoints),
		slog.Int("total_book_files", plan.TotalBookFiles),
		slog.Int("owning_books", plan.OwningBooks),
		slog.Any("by_class", plan.ByClass),
	)
	const sample = 50
	for i := range plan.Items {
		if i >= sample {
			break
		}
		it := plan.Items[i]
		_ = reporter.Log(slog.LevelInfo, "orphan",
			slog.String("book_file_id", it.BookFileID),
			slog.String("orphan_book_id", it.OrphanBookID),
			slog.String("file_path", it.FilePath),
			slog.String("class", it.Class),
			slog.String("target_book_id", it.TargetBookID),
			slog.String("detail", it.Detail),
		)
	}
	msg := fmt.Sprintf("Orphan repoint plan: %d orphan row(s) across %d missing book(s); %d repoint(s) planned; nothing written",
		plan.Orphans, plan.OrphanBookIDs, plan.PlannedRepoints)
	prog.Done(msg)
	return nil
}

// planOrphanRepoints classifies every orphan row. Read-only.
//
// Work is partitioned by orphan BookID — one worker owns every row of one
// missing book — so the per-book tombstone/version-group lookup is done once
// and no two workers touch the same book. Results merge under one mutex.
func planOrphanRepoints(ctx context.Context, store orphanRepointPlanStore, orphans []database.BookFileCore) (*OrphanRepointPlan, error) {
	byBook := make(map[string][]database.BookFileCore)
	for _, f := range orphans {
		byBook[f.BookID] = append(byBook[f.BookID], f)
	}
	ids := make([]string, 0, len(byBook))
	for id := range byBook {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	plan := &OrphanRepointPlan{
		Orphans:       len(orphans),
		OrphanBookIDs: len(byBook),
		ByClass:       map[string]int{},
		Items:         make([]OrphanRepointPlanItem, 0, len(orphans)),
	}
	built, err := store.BookAtPathIndexBuilt()
	if err != nil {
		return nil, fmt.Errorf("book_atpath index status: %w", err)
	}
	checkPathOwner := built
	if !built {
		plan.PathOwnerSkipped = "book_atpath index is not built (run its backfill first): the live-book-at-path check was skipped for every row, so no repoint_to_path_owner or ambiguous_path_owner is planned"
	}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for _, bookID := range ids {
		rows := byBook[bookID]
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			survivor, survivorWhy, err := orphanMergeSurvivor(store, bookID)
			if err != nil {
				return err
			}
			items := make([]OrphanRepointPlanItem, 0, len(rows))
			for _, f := range rows {
				it, err := classifyOrphanRow(store, f, survivor, survivorWhy, checkPathOwner)
				if err != nil {
					return err
				}
				items = append(items, it)
			}
			mu.Lock()
			for _, it := range items {
				plan.ByClass[it.Class]++
				if it.TargetBookID != "" {
					plan.PlannedRepoints++
				}
				plan.Items = append(plan.Items, it)
			}
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	sort.Slice(plan.Items, func(i, j int) bool {
		if plan.Items[i].OrphanBookID != plan.Items[j].OrphanBookID {
			return plan.Items[i].OrphanBookID < plan.Items[j].OrphanBookID
		}
		return plan.Items[i].BookFileID < plan.Items[j].BookFileID
	})
	// Two orphan rows for the same file planned onto the same book would give
	// it that file twice. Workers are partitioned by orphan book, so this is
	// resolved here, in the deterministic order above: the first keeps the
	// plan, the rest are duplicate_of_orphan.
	planned := map[[2]string]string{}
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.TargetBookID == "" || it.FilePath == "" {
			continue
		}
		k := [2]string{it.TargetBookID, it.FilePath}
		if first, dup := planned[k]; dup {
			plan.ByClass[it.Class]--
			plan.PlannedRepoints--
			it.Class, it.TargetBookID = orphanClassDupOrphan, ""
			it.Detail = "orphan row " + first + " for the same file is already planned onto that book"
			plan.ByClass[it.Class]++
			continue
		}
		planned[k] = it.BookFileID
	}
	return plan, nil
}

// orphanMergeSurvivor returns the live book the missing book was merged into,
// if its tombstone says so: MergedIntoBookID when that book is live, else the
// single live primary of its version group. Empty when there is no evidence.
func orphanMergeSurvivor(store orphanRepointPlanStore, bookID string) (string, string, error) {
	if bookID == "" {
		return "", "", nil
	}
	tomb, err := store.GetBookTombstone(bookID)
	if err != nil {
		return "", "", fmt.Errorf("tombstone %s: %w", bookID, err)
	}
	if tomb == nil {
		return "", "", nil
	}
	if tomb.MergedIntoBookID != nil && *tomb.MergedIntoBookID != "" {
		b, err := store.GetBookByID(*tomb.MergedIntoBookID)
		if err != nil {
			return "", "", fmt.Errorf("merged-into book %s: %w", *tomb.MergedIntoBookID, err)
		}
		if b != nil && !isSoftDeleted(b) {
			return b.ID, "tombstone merged_into_book_id", nil
		}
	}
	if tomb.VersionGroupID != nil && *tomb.VersionGroupID != "" {
		members, err := store.GetBooksByVersionGroup(*tomb.VersionGroupID)
		if err != nil {
			return "", "", fmt.Errorf("version group %s: %w", *tomb.VersionGroupID, err)
		}
		var primaries []string
		for i := range members {
			m := &members[i]
			if m.ID == bookID || isSoftDeleted(m) {
				continue
			}
			if m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
				primaries = append(primaries, m.ID)
			}
		}
		if len(primaries) == 1 {
			return primaries[0], "tombstone version group primary", nil
		}
	}
	return "", "", nil
}

func classifyOrphanRow(store orphanRepointPlanStore, f database.BookFileCore, survivor, survivorWhy string, checkPathOwner bool) (OrphanRepointPlanItem, error) {
	it := OrphanRepointPlanItem{BookFileID: f.ID, OrphanBookID: f.BookID, FilePath: f.FilePath}
	if f.FilePath != "" {
		// Every row at the path, not the single-valued index's one: the index
		// may name this orphan itself while a live book's row for the same
		// file exists. Fail closed — an incomplete answer here could plan a
		// second row for a file its owner already has.
		rows, err := store.BookFilesAtPath(f.FilePath)
		if err != nil {
			return it, fmt.Errorf("rows at %s: %w", f.FilePath, err)
		}
		for _, other := range rows {
			if other.ID == f.ID || other.BookID == f.BookID {
				continue
			}
			owner, err := store.GetBookByID(other.BookID)
			if err != nil {
				return it, fmt.Errorf("book %s: %w", other.BookID, err)
			}
			if owner != nil {
				it.Class = orphanClassDuplicate
				it.Detail = fmt.Sprintf("path already owned by book_file %s of book %s", other.ID, other.BookID)
				return it, nil
			}
		}
		if checkPathOwner {
			owners := map[string]struct{}{}
			for _, p := range []string{f.FilePath, filepath.Dir(f.FilePath)} {
				ids, err := store.LiveBookIDsAtPath(p)
				if err != nil {
					return it, fmt.Errorf("live books at %s: %w", p, err)
				}
				for _, id := range ids {
					owners[id] = struct{}{}
				}
			}
			switch {
			case len(owners) == 1:
				for id := range owners {
					it.Class, it.TargetBookID = orphanClassPathOwner, id
				}
				it.Detail = "the only live book at the file or its directory"
				return it, nil
			case len(owners) > 1:
				it.Class = orphanClassAmbiguousOwner
				it.Detail = fmt.Sprintf("%d live books at the file or its directory", len(owners))
				return it, nil
			}
		}
	}
	if survivor != "" {
		it.Class, it.TargetBookID, it.Detail = orphanClassMergeSurvivor, survivor, survivorWhy
		return it, nil
	}
	it.Class = orphanClassUnresolved
	return it, nil
}

func isSoftDeleted(b *database.Book) bool {
	return b.MarkedForDeletion != nil && *b.MarkedForDeletion
}
