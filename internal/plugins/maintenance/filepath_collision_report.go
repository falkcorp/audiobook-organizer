// file: internal/plugins/maintenance/filepath_collision_report.go
// version: 1.0.0
// guid: 80fcbf6b-a38f-4f44-86f4-7f1f06d2a0d2
// last-edited: 2026-09-11

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- filepath-collision-report ---
//
// 🔴 WHY THIS EXISTS. TODO.md's `Book.FilePath` collision finding: 1,264
// distinct FilePath values are shared by more than one book row (4,353 of
// 63,870 rows at the time it was measured, 6.8%). Today that happens not to
// bite anything -- the chapters-backfill fallback sampled 88 recoverable rows
// and 0 were among the 4,353 -- but that is a property of TODAY's data, not a
// guarantee that survives the next op that starts reading Book.FilePath as an
// identity signal. The finding explicitly demands the count be RE-RUN before
// Book.FilePath is trusted by any op that WRITES a book row (it directly gates
// missing-file-repoint ever using it as a repoint source).
//
// 🔴 REPORT-ONLY, DELIBERATELY. This op never requests CapLibraryWrite and
// never mutates a book, book_file, or file on disk. Its only output is counts
// and a capped sample of the colliding groups, for a human to read before
// deciding whether a write path may lean on Book.FilePath.
//
// Modeled on missing_file_audit.go's structure (dry-run-by-default report op,
// no writes) and reuses GetAllBooksCore rather than inventing a parallel
// enumeration helper.

// filePathCollisionSampleLimit bounds how many example collision groups are
// surfaced, so the report stays readable when thousands of paths collide.
const filePathCollisionSampleLimit = 50

// filePathCollisionReportParams are the JSON parameters accepted by the op.
type filePathCollisionReportParams struct {
	// SampleLimit overrides how many example collision groups are reported
	// (0 = the default).
	SampleLimit int `json:"sample_limit"`
}

// filePathCollisionSample is one example collision group: a FilePath shared by
// more than one book, with the IDs of every book that shares it.
type filePathCollisionSample struct {
	FilePath string   `json:"file_path"`
	BookIDs  []string `json:"book_ids"`
}

// filePathCollisionReport is the outcome of one sweep.
type filePathCollisionReport struct {
	// TotalBooks is every row GetAllBooksCore returned, before any exclusion.
	TotalBooks int

	// EmptyFilePath counts rows whose FilePath is blank. These are excluded
	// from the collision map entirely -- see the edge-case note below -- and
	// are reported as their own bucket rather than folded into either arm.
	EmptyFilePath int

	// DistinctPaths is the number of distinct non-empty FilePath values seen
	// (owned by exactly one book or by several).
	DistinctPaths int

	// CollisionGroups is the number of distinct FilePath values owned by MORE
	// THAN ONE book -- this is the "1,264 values" figure.
	CollisionGroups int

	// AffectedRows is the total number of book rows that belong to a
	// collision group -- this is the "4,353 of 63,870 rows" figure. A book
	// counts once per FilePath it shares, which is exactly what "rows sharing
	// the same path" means; a book cannot appear in more than one group since
	// each book has exactly one FilePath.
	AffectedRows int

	// Sample holds up to filePathCollisionSampleLimit example groups, sorted
	// by FilePath for a deterministic, diffable report.
	Sample []filePathCollisionSample
}

// collisionRate returns AffectedRows / TotalBooks as a human-readable
// percentage, guarding the empty sweep.
func (r filePathCollisionReport) collisionRate() string {
	if r.TotalBooks == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(r.AffectedRows)/float64(r.TotalBooks))
}

func (r filePathCollisionReport) summary() string {
	return fmt.Sprintf(
		"total_books=%d empty_file_path=%d distinct_paths=%d collision_groups=%d affected_rows=%d (%s)",
		r.TotalBooks, r.EmptyFilePath, r.DistinctPaths, r.CollisionGroups, r.AffectedRows, r.collisionRate())
}

// filePathCollisionStore is the narrow read surface this op needs, per the
// store_slices.go convention of listing methods explicitly rather than
// embedding a wider sub-interface.
type filePathCollisionStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
}

func (p *Plugin) filePathCollisionReportDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.filepath-collision-report",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Book.FilePath collision report",
		Description: "Enumerates every book's FilePath and reports how many books share a " +
			"FilePath with another book -- the population a future write path must check " +
			"before trusting Book.FilePath as an identity signal (e.g. as a repoint source). " +
			"REPORT-ONLY: takes no action and modifies nothing.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.filepath-collision-report",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		// 🔴 READ ONLY. No CapLibraryWrite is ever requested, so the op cannot
		// write even if a future edit tried to.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runFilePathCollisionReport,
	}
}

func (p *Plugin) runFilePathCollisionReport(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params filePathCollisionReportParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	report, err := buildFilePathCollisionReport(ctx, store, params, reporter)
	if err != nil {
		return err
	}

	log := reporter.Logger()
	for _, s := range report.Sample {
		log.Info("filepath-collision-report: collision group", "file_path", s.FilePath, "book_ids", s.BookIDs)
	}
	log.Info("filepath-collision-report complete",
		"total_books", report.TotalBooks, "empty_file_path", report.EmptyFilePath,
		"distinct_paths", report.DistinctPaths, "collision_groups", report.CollisionGroups,
		"affected_rows", report.AffectedRows, "collision_rate", report.collisionRate())
	return nil
}

// filePathCollisionShardCount bounds the fan-out of the collision sweep to
// runtime.NumCPU(), per CLAUDE.md's whole-library concurrency mandate: this
// loop iterates a whole-library-scale collection (63,870+ rows measured
// 2026-09), so it gets a bounded worker pool rather than a single-threaded
// `for range books` -- see
// docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md.
func filePathCollisionShardCount() int {
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	return n
}

// filePathCollisionShard is one contiguous slice of the book list, handed to a
// single worker. Sharding the OUTER loop (rather than only parallelizing
// per-book work) is the CLAUDE.md-mandated shape for a loop whose reduce step
// writes into shared mutable state (the path -> book-IDs map): each worker
// builds its own LOCAL map with no locking at all, and the shared map is
// touched only once per shard, under a single mutex, at the reduce step.
type filePathCollisionShard struct {
	idx   int
	books []database.BookCore
}

// shardBookCores splits books into up to n contiguous, roughly-equal shards.
// Returns fewer than n shards if there are fewer books than workers, and never
// returns an empty shard.
func shardBookCores(books []database.BookCore, n int) [][]database.BookCore {
	if n < 1 {
		n = 1
	}
	if len(books) == 0 {
		return nil
	}
	if n > len(books) {
		n = len(books)
	}
	shards := make([][]database.BookCore, 0, n)
	base := len(books) / n
	rem := len(books) % n
	start := 0
	for i := 0; i < n; i++ {
		size := base
		if i < rem {
			size++
		}
		if size == 0 {
			continue
		}
		shards = append(shards, books[start:start+size])
		start += size
	}
	return shards
}

// buildFilePathCollisionReport performs the sweep and RETURNS the report.
//
// Split out from the op body so the counts can be asserted as values rather
// than scraped from a log line -- this op's entire purpose is the numbers it
// produces, and any op that later relies on them deserves a report tested
// directly.
func buildFilePathCollisionReport(ctx context.Context, store filePathCollisionStore, params filePathCollisionReportParams, reporter sdk.Reporter) (filePathCollisionReport, error) {
	sampleLimit := params.SampleLimit
	if sampleLimit <= 0 {
		sampleLimit = filePathCollisionSampleLimit
	}
	log := reporter.Logger()
	log.Info("filepath-collision-report start", "sample_limit", sampleLimit)

	// One limit-0 call = one consistent snapshot. Paging with offset across
	// multiple calls can skip or repeat rows if the memdb snapshot swaps
	// between pages (reconcile #2443) -- title_backfill.go and its siblings
	// made the same choice for the same reason.
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return filePathCollisionReport{}, fmt.Errorf("GetAllBooksCore: %w", err)
	}

	shards := shardBookCores(books, filePathCollisionShardCount())
	items := make([]filePathCollisionShard, len(shards))
	for i, s := range shards {
		items[i] = filePathCollisionShard{idx: i, books: s}
	}

	byPath := make(map[string][]string, len(books))
	var mergeMu sync.Mutex
	var emptyPath atomic.Int64

	if len(items) > 0 {
		prog := sdk.NewProgress(reporter, len(items))
		prog.Start(fmt.Sprintf("Scanning %d book(s) across %d shard(s) for FilePath collisions…", len(books), len(items)))

		err = registry.RunItems(ctx, reporter, items, func(_ context.Context, it filePathCollisionShard) error {
			local := make(map[string][]string)
			localEmpty := 0
			for _, b := range it.books {
				path := strings.TrimSpace(b.FilePath)
				// A blank FilePath is not a "collision" -- every blank row would
				// otherwise pile into one giant, meaningless group. Counted
				// separately instead; see filePathCollisionReport.EmptyFilePath.
				if path == "" {
					localEmpty++
					continue
				}
				local[path] = append(local[path], b.ID)
			}
			// The reduce step: the only place this loop touches the shared map,
			// exactly once per shard, under one mutex.
			mergeMu.Lock()
			for path, ids := range local {
				byPath[path] = append(byPath[path], ids...)
			}
			mergeMu.Unlock()
			emptyPath.Add(int64(localEmpty))
			return nil
		}, registry.RunItemsOptions{
			Concurrency: filePathCollisionShardCount(),
			ErrMode:     registry.ErrModeCollect,
			Label: func(i, t int) string {
				return fmt.Sprintf("Scanned shard %d/%d", i+1, t)
			},
		})
		if err != nil {
			return filePathCollisionReport{}, fmt.Errorf("collision sweep: %w", err)
		}
	}

	report := filePathCollisionReport{
		TotalBooks:    len(books),
		EmptyFilePath: int(emptyPath.Load()),
		DistinctPaths: len(byPath),
	}

	// Sorted so the report (and its sample) is deterministic and diffable
	// across runs rather than depending on map iteration order or which
	// worker finished first.
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		ids := byPath[path]
		if len(ids) <= 1 {
			continue
		}
		report.CollisionGroups++
		report.AffectedRows += len(ids)
		if len(report.Sample) < sampleLimit {
			sorted := append([]string(nil), ids...)
			sort.Strings(sorted)
			report.Sample = append(report.Sample, filePathCollisionSample{FilePath: path, BookIDs: sorted})
		}
	}

	if len(items) > 0 {
		sdk.NewProgress(reporter, len(items)).Done("REPORT ONLY (nothing modified) — " + report.summary())
	} else {
		_ = reporter.UpdateProgress(0, 0, "REPORT ONLY (nothing modified) — "+report.summary())
	}
	return report, nil
}
