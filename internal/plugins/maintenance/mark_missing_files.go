// file: internal/plugins/maintenance/mark_missing_files.go
// version: 1.9.0
// guid: 3d7a9c14-6e28-4f5b-b0a3-1c9e5d827f46
// last-edited: 2026-09-19

// Package maintenance — MARK missing book_file rows by reconciling the stored
// book_file.Missing flag with what is actually on disk.
//
// 🔴 WHY THIS EXISTS. The dashboard's "Broken Files" counter reads a persisted
// flag, not a live stat — stat-ing every one of ~726k rows against the NAS on
// each stats refresh is exactly the 2-hour cost maintenance.missing-file-audit
// pays asynchronously, and cannot be paid on a dashboard load. So the counter is
// only as honest as the flag. Today the flag has effectively no live writer
// (the sole non-test setter is acoustid/backfill.go), so the counter reports 0
// on a library that in fact has ~16k books with gone bytes. This op is the
// writer: it stats every row and sets Missing to match disk, so the counter
// (BrokenFiles = distinct primary books with ≥1 Missing file) becomes true.
//
// 🔴 RECONCILES BOTH DIRECTIONS. It sets Missing=true where bytes are gone AND
// clears Missing=false where a row was flagged but the bytes are present again
// (e.g. after a repoint or a re-organize restored the path). A one-directional
// mark would let the counter drift permanently high after a repair.
//
// This op WRITES only the Missing boolean. It never moves a file, never deletes
// a row, never touches FilePath. The write goes through UpdateBookFiles with
// UpdateBookFile's full-record-replacement semantics (rehydrate → mutate one
// field → write back), so the fingerprint, transcript and tags on the row are
// preserved. One book's flips are written in ONE call, so the book's aggregates
// are recomputed once rather than once per flipped row — see
// bookfile_batch_write.go for why that matters.
//
// ⚠️ SCAN INTERLOCK (enforced at runtime as of PR #3080). On apply=true this op
// cooperatively stands the library scanner down for its write phase (acquires the
// scan stand-down, renews it per write, aborts the run if the lease lapses), so a
// scan can no longer mutate the same rows underneath the write. This replaces the
// old "operational, not enforced" precondition. As a second line of defense the
// op also re-stats every row of a book immediately before writing that book, so a
// row whose disk state changed since the plan phase is skipped rather than written
// stale. The stat and the write are one book apart, not one row apart. Missing
// is a boolean the next run reconciles, so any transient wrong value is
// self-healing regardless.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type markMissingParams struct {
	// Apply must be explicitly true to write. Default false = report only.
	Apply bool `json:"apply"`
	// PathPrefix scopes the sweep to one tree (e.g. only the organizer's tree).
	// The match is on a folder boundary: "/lib" matches "/lib" and "/lib/…" but
	// not "/lib2/…". Empty = no filter.
	//
	// Accepted as "pathPrefix" OR "path_prefix" (the latter lands in
	// PathPrefixAlias and is folded in by decodeOpParams; both present with
	// different values is an error). Unknown keys are rejected.
	PathPrefix string `json:"pathPrefix"`
	// PathPrefixAlias is the "path_prefix" spelling of PathPrefix. Never read
	// it directly: decodeOpParams folds it into PathPrefix and clears it.
	PathPrefixAlias string `json:"path_prefix,omitempty"`
	// Max bounds how many rows one run will FLIP. <=0 means unbounded: unlike
	// missing-file-repoint (which samples), a partial mark leaves the counter
	// partially honest, which is worse than either extreme — so the default is
	// to reconcile the whole library. The dry run reports the full would-flip
	// count first, so the write cost (one new book_file version per flipped row,
	// on the CoW store) is visible before apply. Max>0 is available for staged
	// runs; flips are taken in a stable file-ID order so a capped run resumes
	// cleanly.
	Max int `json:"max"`
	// ReportPath overrides where the full per-row TSV lands. Empty derives a
	// path under {root_dir}/.reports/. Written on EVERY run — a dry run whose decisions are
	// unreadable cannot inform the apply it exists to inform.
	ReportPath string `json:"reportPath,omitempty"`
}

// markDecision is one row's outcome. Every scanned row lands in exactly one
// bucket and every bucket is reported, so a row that is NOT flipped is visible.
type markDecision struct {
	FileID string `json:"file_id"`
	BookID string `json:"book_id"`
	// Bucket is the coarse outcome; Reason carries specifics. Buckets:
	// "mark-missing" (present flag false → bytes gone → set true),
	// "clear-stale" (flag true → bytes present → set false),
	// "unchanged" (flag already matches disk), "unreadable" (stat failed for a
	// reason other than not-exist — left as-is), "skipped-changed" (the
	// write-time re-stat disagreed with the plan; skipped).
	Bucket string `json:"bucket"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type markMissingPlan struct {
	Apply       bool `json:"apply"`
	ScannedRows int  `json:"scanned_rows"`
	// WouldMarkMissing / WouldClearStale are the flips the plan found; MarkedMissing
	// / ClearedStale are what apply actually wrote (0 on a dry run).
	WouldMarkMissing int `json:"would_mark_missing"`
	WouldClearStale  int `json:"would_clear_stale"`
	MarkedMissing    int `json:"marked_missing"`
	ClearedStale     int `json:"cleared_stale"`
	Unchanged        int `json:"unchanged"`
	Unreadable       int `json:"unreadable"`
	SkippedChanged   int `json:"skipped_changed"`
	UpdateErrs       int `json:"update_errs"`
	// RecomputeErrs counts BOOKS whose rows were written but whose aggregate
	// recompute then failed. Deliberately separate from UpdateErrs: those rows
	// ARE written, so re-running them would be wrong — but a run that reported
	// only update_errs=0 would leave the operator with "some unknown set of
	// books has stale totals, go grep the logs".
	RecomputeErrs int `json:"recompute_errs"`
	CappedAt      int `json:"capped_at,omitempty"`

	// BooksNowBroken is the distinct-primary-book count the dashboard's BrokenFiles
	// counter will show after this run — i.e. books with ≥1 row that IS missing on
	// disk (whether this run flipped it or it was already flagged). Reported so the
	// operator can compare it against the dashboard without re-deriving it.
	BooksBrokenOnDisk int `json:"books_broken_on_disk"`

	ReportPath string `json:"report_path,omitempty"`

	// Samples is a per-bucket-capped subset for the JSON log line; all holds every
	// decision for the TSV (not serialised).
	Samples       []markDecision `json:"samples,omitempty"`
	all           []markDecision
	bucketSampled map[string]int
}

func (p *markMissingPlan) record(d markDecision) {
	p.all = append(p.all, d)
	const samplesPerBucket = 8
	if p.bucketSampled == nil {
		p.bucketSampled = map[string]int{}
	}
	if p.bucketSampled[d.Bucket] < samplesPerBucket {
		p.bucketSampled[d.Bucket]++
		p.Samples = append(p.Samples, d)
	}
}

func (p markMissingPlan) summary() string {
	mode := "DRY RUN"
	if p.Apply {
		mode = "APPLIED"
	}
	return fmt.Sprintf(
		"%s scanned=%d | mark-missing: would=%d wrote=%d | clear-stale: would=%d wrote=%d | unchanged=%d unreadable=%d skipped-changed=%d update_errs=%d recompute_errs=%d | books_broken_on_disk=%d",
		mode, p.ScannedRows, p.WouldMarkMissing, p.MarkedMissing,
		p.WouldClearStale, p.ClearedStale, p.Unchanged, p.Unreadable,
		p.SkippedChanged, p.UpdateErrs, p.RecomputeErrs, p.BooksBrokenOnDisk)
}

func (p *Plugin) markMissingFilesDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.mark-missing-files",
		DisplayName: "Mark missing book files (reconcile the Missing flag)",
		Description: "Stats every book_file row and reconciles its stored Missing flag with disk: sets " +
			"Missing=true where the bytes are gone, clears it where they are present again. This is what " +
			"makes the dashboard's Broken Files counter true (it reads the flag, not a live stat). WRITES " +
			"only the Missing boolean — never moves or deletes anything. Default dry-run; pass {\"apply\": " +
			"true} to write. On apply it cooperatively stands the library scanner down for the write phase " +
			"(PR #3080) and re-stats each row before writing, so a concurrent scan can no longer clobber the write.",
		DefaultPriority: sdk.PriorityLow,
		// Its OWN ConcurrencyKey, like every other maintenance op. It deliberately
		// does NOT share "library.scan"'s key and declares no Writes: library.scan
		// declares no Writes either, so a Writes conflict-set would gate against
		// nothing (Gate 3b is Writes∩Writes). The scan/apply interlock is the runtime
		// scan stand-down — see the SCAN INTERLOCK note at the top of this file.
		ConcurrencyKey: "maintenance.mark-missing-files",
		// ResumeDrop, matching the other missing-file ops: this WRITES, and an apply
		// interrupted midway must not silently resume. Re-running is cheap and safe
		// (a reconciled row is simply not flipped again), so dropping loses nothing.
		ResumePolicy: sdk.ResumeDrop,
		Liveness:     sdk.LivenessRunItems,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runMarkMissingFiles(ctx, raw, reporter)
		},
	}
}

func (p *Plugin) runMarkMissingFiles(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params markMissingParams
	if err := decodeOpParams(rawParams, &params); err != nil {
		return fmt.Errorf("mark-missing-files: decode params: %w", err)
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	log := reporter.Logger()
	if params.Apply {
		// The apply phase now cooperatively stands the library scanner down for its
		// duration (PR #3080) — the real control that replaces the old "no scan
		// should be active" documented precondition. The write-time re-stat remains
		// as a second line of defense so a concurrent change is skipped, not written.
		log.Warn("mark-missing-files: APPLY — writing the Missing flag. The library scan is stood down " +
			"for the write phase and each row is re-stat'd before writing.")
	}

	// Resolve the report path before any work: with no reportPath and no usable
	// root_dir the run must fail with nothing done, not apply and then lose its record.
	reportPath, rpErr := p.resolveReportPath(params.ReportPath, opReportFileName(reporter, "mark-missing-files"))
	if rpErr != nil {
		return fmt.Errorf("mark-missing-files: %w", rpErr)
	}
	plan, err := planMarkMissingFiles(ctx, store, p.deps, params, reporter)

	// Write the report BEFORE returning any error, so a run that aborts mid-apply
	// (e.g. the scan stand-down lease lapsed after k writes) still leaves the
	// per-row artifact — exactly the run where an operator most needs it. The
	// planning phase populates plan.all in full regardless of how the write phase
	// ends; skip the write only for an early error that produced no decisions.
	if err == nil || len(plan.all) > 0 {
		if wErr := writeMarkMissingReport(reportPath, plan.all); wErr != nil {
			log.Error("mark-missing-files: FAILED to write the per-row report",
				"path", reportPath, "err", wErr, "rows", len(plan.all))
		} else {
			plan.ReportPath = reportPath
			log.Info("mark-missing-files: per-row report written", "path", reportPath, "rows", len(plan.all))
		}
	}

	if err != nil {
		return err
	}

	if b, mErr := json.Marshal(plan); mErr == nil {
		log.Info("mark-missing-files report (JSON)", "report", string(b))
	}
	if plan.CappedAt > 0 {
		log.Warn("mark-missing-files: more rows to flip than the cap — run again to continue",
			"cap", plan.CappedAt, "would_mark_missing", plan.WouldMarkMissing, "would_clear_stale", plan.WouldClearStale)
	}
	log.Info("mark-missing-files complete", "summary", plan.summary())
	return nil
}

// markMissingStore is the narrow store this op needs: read every row's core
// projection (path + current Missing), read the book core projection to learn
// which books are PRIMARY versions (so BooksBrokenOnDisk counts the same
// population the dashboard's BrokenFiles counter does — primary books only),
// rehydrate one book's files to write the full record back, and write it.
type markMissingStore interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	// UpdateBookFiles, not per-row UpdateBookFile: the apply phase rewrites
	// many rows of the same book and must recompute that book's aggregates
	// once after the rows, not once per row (see bookfile_batch_write.go).
	UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (int, error)
}

// flip is one row whose Missing flag disagrees with disk.
type flip struct {
	file    database.BookFileCore
	toValue bool // the Missing value to write
}

func planMarkMissingFiles(ctx context.Context, store markMissingStore, scan ScanController, params markMissingParams, reporter sdk.Reporter) (markMissingPlan, error) {
	log := reporter.Logger()
	log.Info("mark-missing-files start",
		"apply", params.Apply, "path_prefix", params.PathPrefix, "max", params.Max)

	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return markMissingPlan{}, fmt.Errorf("load book files: %w", err)
	}
	plan := markMissingPlan{Apply: params.Apply}

	// primaryBookIDs is the set of PRIMARY-version books. BooksBrokenOnDisk must
	// count only these, because the dashboard's BrokenFiles counter does
	// (pebble_store_stats.go / memdb_reads.go both skip non-primary books before
	// tallying Missing). Counting every book here — including redundant version
	// rows — would make the op predict a number materially higher than the tile it
	// claims to predict, and an operator comparing the two would read the gap as a
	// half-failed apply. A book is primary when IsPrimaryVersion is nil or true,
	// matching the stats derivation exactly. Loaded in bounded pages like repoint.
	primaryBookIDs := make(map[string]struct{})
	for offset := 0; ; offset += bookPageSize {
		page, perr := store.GetAllBooksCore(bookPageSize, offset)
		if perr != nil {
			return markMissingPlan{}, fmt.Errorf("load books: %w", perr)
		}
		for i := range page {
			if page[i].IsPrimaryVersion == nil || *page[i].IsPrimaryVersion {
				primaryBookIDs[page[i].ID] = struct{}{}
			}
		}
		if len(page) < bookPageSize {
			break
		}
	}

	type item struct {
		idx  int
		file database.BookFileCore
	}
	items := make([]item, 0, len(files))
	for i := range files {
		path := strings.TrimSpace(files[i].FilePath)
		// A row with no path is a different defect; this op reconciles bytes-on-disk
		// against the flag, and a pathless row has no bytes to check.
		if path == "" {
			continue
		}
		if !pathPrefixMatches(path, params.PathPrefix) {
			continue
		}
		items = append(items, item{idx: len(items), file: files[i]})
	}
	plan.ScannedRows = len(items)

	// Phase 1 — stat every row and record its disk state. I/O bound over the whole
	// library, so it runs on the same bounded pool the audit/repoint sweeps use.
	type rowState struct {
		gone       bool // bytes are NOT on disk (IsNotExist)
		unreadable bool // stat failed for another reason — leave the flag untouched
	}
	states := make([]rowState, len(items))
	var missingCount atomic.Int64

	prog := sdk.NewProgress(reporter, len(items))
	prog.Start(fmt.Sprintf("Checking %d book_file path(s)…", len(items)))

	err = registry.RunItems(ctx, reporter, items, func(_ context.Context, it item) error {
		switch _, serr := os.Stat(it.file.FilePath); {
		case serr == nil:
			// present
		case os.IsNotExist(serr):
			states[it.idx] = rowState{gone: true}
			missingCount.Add(1)
		default:
			states[it.idx] = rowState{unreadable: true}
			log.Warn("mark-missing-files: could not stat", "path", it.file.FilePath, "err", serr)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Checked %d/%d paths (gone=%d)", i+1, t, missingCount.Load())
		},
	})
	if err != nil {
		return markMissingPlan{}, fmt.Errorf("stat sweep: %w", err)
	}

	// Phase 2 — classify each row and collect the flips. Serial: a per-row decision
	// over the results slice, no shared state.
	booksBrokenOnDisk := make(map[string]struct{})
	var flips []flip
	for i := range items {
		it := items[i]
		st := states[i]
		if st.gone {
			// Count only primary books — the population the BrokenFiles tile shows.
			if _, ok := primaryBookIDs[it.file.BookID]; ok {
				booksBrokenOnDisk[it.file.BookID] = struct{}{}
			}
		}
		switch {
		case st.unreadable:
			plan.Unreadable++
			plan.record(markDecision{FileID: it.file.ID, BookID: it.file.BookID,
				Bucket: "unreadable", Path: it.file.FilePath,
				Reason: "stat failed for a reason other than not-exist; flag left unchanged"})
		case st.gone && !it.file.Missing:
			plan.WouldMarkMissing++
			flips = append(flips, flip{file: it.file, toValue: true})
		case !st.gone && it.file.Missing:
			plan.WouldClearStale++
			flips = append(flips, flip{file: it.file, toValue: false})
		default:
			plan.Unchanged++
		}
	}
	plan.BooksBrokenOnDisk = len(booksBrokenOnDisk)

	// Deterministic order so a capped run takes a stable prefix across re-runs.
	sort.Slice(flips, func(a, b int) bool { return flips[a].file.ID < flips[b].file.ID })
	if params.Max > 0 && len(flips) > params.Max {
		plan.CappedAt = params.Max
		log.Warn("mark-missing-files: more flips than the cap — taking the first N by file ID",
			"flips", len(flips), "cap", params.Max)
		flips = flips[:params.Max]
	}

	for _, fl := range flips {
		bucket, reason := "mark-missing", "bytes gone → set Missing=true"
		if !fl.toValue {
			bucket, reason = "clear-stale", "bytes present → clear Missing=false"
		}
		plan.record(markDecision{FileID: fl.file.ID, BookID: fl.file.BookID,
			Bucket: bucket, Path: fl.file.FilePath, Reason: reason})
	}

	if !params.Apply {
		log.Info("mark-missing-files: DRY RUN — no rows written",
			"would_mark_missing", plan.WouldMarkMissing, "would_clear_stale", plan.WouldClearStale)
		return plan, nil
	}

	// Acquire the scan stand-down for the write phase: both this op and a running
	// library.scan write the Missing flag on the same rows, so quiesce the scanner
	// to keep our flips from racing its. Released (scan resumes) on return; dry-run
	// returned above so it never stands the scanner down.
	holderID, standDownHeld, releaseStandDown, sdErr := acquireScanStandDownForApply(ctx, scan, reporter, "mark-missing-files apply")
	if sdErr != nil {
		return plan, fmt.Errorf("mark-missing-files: acquire scan stand-down: %w", sdErr)
	}
	defer releaseStandDown()

	// Phase 3 — write. Rehydrate the FULL BookFile and change only Missing:
	// UpdateBookFile semantics are a full-record replacement, so a partial record
	// would wipe the fingerprint/transcript/tags. Re-stat immediately before
	// writing (the interlock): if disk no longer agrees with the planned value,
	// skip the row rather than write a value a concurrent scan just invalidated.
	//
	// The work item is a BOOK, not a flip. A flat per-flip loop did one
	// GetBookFiles AND one aggregate recompute per flip, each re-reading every
	// row of the book: O(n^2) for a book many of whose rows flip, and this op is
	// uncapped by default over a library with ~66k missing rows and books of up
	// to 1,494 files. Grouped, each book pays one read and one recompute.
	// Grouping by book also makes the pool's partitions disjoint, so two workers
	// can never race each other's recompute of the same book.
	var markedMissing, clearedStale, skippedChanged, updateErrs, recomputeErrs, rowsDone, booksDone atomic.Int64
	var standDownLost atomic.Bool
	totalFlips := len(flips)
	groups := groupItemsByBook(flips,
		func(fl flip) string { return fl.file.BookID },
		func(fl flip) string { return fl.file.ID })
	totalBooks := len(groups)
	err = registry.RunItems(ctx, reporter, groups, func(itemCtx context.Context, g bookFileBatchGroup[flip]) error {
		// RunItems reports progress in BOOKS (the item). The in-book progress
		// line below must use the same units or the bar alternates between two
		// scales and appears to jump backwards -- see run_items.go's P-2 note.
		// The row counts live in the message text instead.
		defer booksDone.Add(1)
		// Heartbeat + hard-abort guard (RunItems does not renew the lease).
		if standDownLost.Load() {
			return nil
		}
		if scanStandDownLostForApply(scan, holderID, standDownHeld) {
			standDownLost.Store(true)
			log.Warn("mark-missing-files: scan stand-down lease lost — aborting remaining writes")
			return nil
		}
		// Interlock: fresh truth from disk for every row of this book. The stat
		// pass runs BEFORE the DB read so that GetBookFiles' snapshot — the one
		// the write is actually built from, and therefore the one whose
		// staleness could lose a concurrent update — is the younger of the two.
		// The window is wider than the old per-row stat-then-write; it is now
		// bounded by one book's stat pass rather than by a single stat.
		wanted := make(map[string]bool, len(g.Items))
		// The stat pass is the longest stretch between two stand-down renewals,
		// and RunItems renews only per BOOK — so stamp liveness and re-check the
		// lease from inside it. See bookFileStatLivenessEvery.
		beat := prewriteHeartbeat(reporter, func() bool {
			if standDownLost.Load() {
				return true
			}
			if scanStandDownLostForApply(scan, holderID, standDownHeld) {
				standDownLost.Store(true)
				log.Warn("mark-missing-files: scan stand-down lease lost — aborting remaining writes")
				return true
			}
			return false
		})
		for fi, fl := range g.Items {
			if !beat(fi) {
				return nil
			}
			switch _, serr := os.Stat(fl.file.FilePath); {
			case serr == nil:
				if fl.toValue { // planned "gone" but present now — disk changed
					skippedChanged.Add(1)
					rowsDone.Add(1)
					continue
				}
			case os.IsNotExist(serr):
				if !fl.toValue { // planned "present" but gone now — disk changed
					skippedChanged.Add(1)
					rowsDone.Add(1)
					continue
				}
			default:
				// Now unreadable; don't write a value we can't stand behind.
				skippedChanged.Add(1)
				rowsDone.Add(1)
				continue
			}
			wanted[fl.file.ID] = fl.toValue
		}
		if len(wanted) == 0 {
			return nil
		}

		siblings, gerr := store.GetBookFiles(g.BookID)
		if gerr != nil {
			// One error per row that would have been written, matching the
			// per-row loop this replaced: each of those rows did its own
			// GetBookFiles and counted its own failure.
			updateErrs.Add(int64(len(wanted)))
			rowsDone.Add(int64(len(wanted)))
			log.Warn("mark-missing-files: load book files", "book", g.BookID, "err", gerr)
			return nil
		}
		byID := make(map[string]*database.BookFile, len(siblings))
		for i := range siblings {
			byID[siblings[i].ID] = &siblings[i]
		}
		rows := make([]*database.BookFile, 0, len(wanted))
		values := make([]bool, 0, len(wanted))
		for _, fl := range g.Items {
			toValue, ok := wanted[fl.file.ID]
			if !ok {
				continue
			}
			full := byID[fl.file.ID]
			if full == nil {
				updateErrs.Add(1)
				rowsDone.Add(1)
				log.Warn("mark-missing-files: row vanished before write", "file", fl.file.ID)
				continue
			}
			if full.Missing == toValue {
				// Already reconciled (a concurrent run or a re-stat already fixed it).
				rowsDone.Add(1)
				continue
			}
			full.Missing = toValue
			rows = append(rows, full)
			values = append(values, toValue)
		}
		if len(rows) == 0 {
			return nil
		}

		bookID := g.BookID
		out := writeBookFileBatch(itemCtx, store, rows, bookFileBatchOpts{
			Reporter: reporter,
			Abort: func() bool {
				if standDownLost.Load() {
					return true
				}
				if scanStandDownLostForApply(scan, holderID, standDownHeld) {
					standDownLost.Store(true)
					log.Warn("mark-missing-files: scan stand-down lease lost — aborting remaining writes")
					return true
				}
				return false
			},
			Progress: func(done, total int) (int, int, string) {
				return int(booksDone.Load()), totalBooks, fmt.Sprintf(
					"book %s: wrote flag %d/%d (reconciled %d/%d, errs=%d)",
					bookID, done, total, rowsDone.Load(), totalFlips, updateErrs.Load())
			},
			OnRow: func(i int, applied bool) {
				rowsDone.Add(1)
				if !applied {
					return
				}
				if values[i] {
					markedMissing.Add(1)
				} else {
					clearedStale.Add(1)
				}
			},
		})
		updateErrs.Add(int64(out.RowErrs))
		recomputeErrs.Add(int64(out.RecomputeErrs))
		if out.RowErrs > 0 {
			log.Warn("mark-missing-files: update failed for some rows",
				"book", bookID, "rows", len(rows), "row_errors", out.RowErrs)
		}
		// Rows committed, book totals possibly stale — NOT an update error.
		if out.RecomputeErrs > 0 {
			log.Warn("mark-missing-files: flags written but the book's aggregates could not be recomputed",
				"book", bookID, "books", out.RecomputeErrs)
		}
		if out.Cancelled {
			log.Warn("mark-missing-files: book's flag writes stopped early (cancelled or stand-down lost)",
				"book", bookID, "planned", len(rows), "written", out.Applied)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		ErrMode:     registry.ErrModeCollect,
		// The items are books now, so the label counts FLIPS from its own
		// atomic rather than the item index, which would read as "reconciled
		// 3/900" for 900 books holding 40,000 flips.
		Label: func(_, books int) string {
			return fmt.Sprintf("Reconciled %d/%d flags across %d books (errs=%d)",
				rowsDone.Load(), totalFlips, books, updateErrs.Load())
		},
	})
	if err != nil {
		return plan, fmt.Errorf("reconcile writes: %w", err)
	}
	plan.MarkedMissing = int(markedMissing.Load())
	plan.ClearedStale = int(clearedStale.Load())
	plan.SkippedChanged = int(skippedChanged.Load())
	plan.UpdateErrs = int(updateErrs.Load())
	plan.RecomputeErrs = int(recomputeErrs.Load())
	if standDownLost.Load() {
		// The per-bucket counts describe only the rows this run ATTEMPTED. An
		// abort abandons the rest of the aborting book and every book after it
		// without counting them anywhere, so marked+cleared+skipped+errs does
		// NOT sum to the planned flip count on an aborted run. Say so here
		// rather than leaving the operator to discover the gap by subtraction.
		return plan, fmt.Errorf("mark-missing-files: scan stand-down lease lapsed mid-apply after %d of %d flips — aborted; the remaining %d were NOT attempted and are not counted in any bucket (re-run after the scan is idle)",
			plan.MarkedMissing+plan.ClearedStale, totalFlips,
			totalFlips-(plan.MarkedMissing+plan.ClearedStale+plan.SkippedChanged+plan.UpdateErrs))
	}
	return plan, nil
}

// writeMarkMissingReport dumps every scanned row's decision, TSV, so a person
// deciding whether to apply can read and grep it by bucket.
func writeMarkMissingReport(path string, decisions []markDecision) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("bucket\tfile_id\tbook_id\tpath\treason\n")
	for _, d := range decisions {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n",
			d.Bucket, d.FileID, d.BookID, clean(d.Path), clean(d.Reason))
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}
