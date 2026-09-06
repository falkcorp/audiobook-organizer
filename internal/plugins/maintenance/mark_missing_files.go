// file: internal/plugins/maintenance/mark_missing_files.go
// version: 1.2.0
// guid: 3d7a9c14-6e28-4f5b-b0a3-1c9e5d827f46
// last-edited: 2026-09-06

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
// a row, never touches FilePath. The write goes through UpdateBookFile as a
// full-record replacement (rehydrate → mutate one field → write back), so the
// fingerprint, transcript and tags on the row are preserved.
//
// ⚠️ SCAN PRECONDITION (operational, not enforced). Do NOT run with apply=true
// while a library scan is active. There is no runtime "is a scan running" query
// to gate on, and Missing is a boolean the next run reconciles, so a transient
// wrong value is self-healing — but a scan mutating rows underneath the write is
// still avoidable churn. The real control is the deploy boundary (the scan only
// resumes on the operator's deploy); this op additionally re-stats each row
// immediately before writing it (the interlock below), so a row whose disk state
// changed since the plan phase is skipped rather than written stale.
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
	PathPrefix string `json:"pathPrefix"`
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
	// path under reports/. Written on EVERY run — a dry run whose decisions are
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
	CappedAt         int `json:"capped_at,omitempty"`

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
		"%s scanned=%d | mark-missing: would=%d wrote=%d | clear-stale: would=%d wrote=%d | unchanged=%d unreadable=%d skipped-changed=%d update_errs=%d | books_broken_on_disk=%d",
		mode, p.ScannedRows, p.WouldMarkMissing, p.MarkedMissing,
		p.WouldClearStale, p.ClearedStale, p.Unchanged, p.Unreadable,
		p.SkippedChanged, p.UpdateErrs, p.BooksBrokenOnDisk)
}

func (p *Plugin) markMissingFilesDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.mark-missing-files",
		DisplayName: "Mark missing book files (reconcile the Missing flag)",
		Description: "Stats every book_file row and reconciles its stored Missing flag with disk: sets " +
			"Missing=true where the bytes are gone, clears it where they are present again. This is what " +
			"makes the dashboard's Broken Files counter true (it reads the flag, not a live stat). WRITES " +
			"only the Missing boolean — never moves or deletes anything. Default dry-run; pass {\"apply\": " +
			"true} to write. Do NOT run with apply=true while a library scan is active (the op re-stats each " +
			"row before writing it, so concurrent changes are skipped rather than written stale, but a scan " +
			"still causes avoidable churn).",
		DefaultPriority: sdk.PriorityLow,
		// Its OWN ConcurrencyKey, like every other maintenance op. It deliberately
		// does NOT share "library.scan"'s key and declares no Writes: library.scan
		// declares no Writes either, so a Writes conflict-set would gate against
		// nothing (Gate 3b is Writes∩Writes). The scan/apply interlock is
		// operational — see the SCAN PRECONDITION note at the top of this file.
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
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("mark-missing-files: decode params: %w", err)
		}
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

	plan, err := planMarkMissingFiles(ctx, store, p.deps, params, reporter)
	if err != nil {
		return err
	}

	// Write the report BEFORE the summary log lines, so a run killed mid-summary
	// still leaves the artifact behind.
	reportPath := params.ReportPath
	if reportPath == "" {
		name := registry.ReporterOpID(reporter)
		if name == "" {
			name = "unknown-op"
		}
		reportPath = filepath.Join("reports", "mark-missing-files-"+name+".tsv")
	}
	if wErr := writeMarkMissingReport(reportPath, plan.all); wErr != nil {
		log.Error("mark-missing-files: FAILED to write the per-row report",
			"path", reportPath, "err", wErr, "rows", len(plan.all))
	} else {
		plan.ReportPath = reportPath
		log.Info("mark-missing-files: per-row report written", "path", reportPath, "rows", len(plan.all))
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
	UpdateBookFile(id string, file *database.BookFile) error
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
		if params.PathPrefix != "" && !strings.HasPrefix(path, params.PathPrefix) {
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
	// UpdateBookFile is a full-record replacement, so a partial record would wipe
	// the fingerprint/transcript/tags. Re-stat immediately before writing (the
	// interlock): if disk no longer agrees with the planned value, skip the row
	// rather than write a value a concurrent scan just invalidated.
	var markedMissing, clearedStale, skippedChanged, updateErrs atomic.Int64
	var standDownLost atomic.Bool
	err = registry.RunItems(ctx, reporter, flips, func(_ context.Context, fl flip) error {
		// Heartbeat + hard-abort guard (RunItems does not renew the lease).
		if standDownLost.Load() {
			return nil
		}
		if scanStandDownLostForApply(scan, holderID, standDownHeld) {
			standDownLost.Store(true)
			log.Warn("mark-missing-files: scan stand-down lease lost — aborting remaining writes")
			return nil
		}
		// Interlock: fresh truth at write time.
		switch _, serr := os.Stat(fl.file.FilePath); {
		case serr == nil:
			if fl.toValue { // planned "gone" but present now — disk changed
				skippedChanged.Add(1)
				return nil
			}
		case os.IsNotExist(serr):
			if !fl.toValue { // planned "present" but gone now — disk changed
				skippedChanged.Add(1)
				return nil
			}
		default:
			// Now unreadable; don't write a value we can't stand behind.
			skippedChanged.Add(1)
			return nil
		}

		siblings, gerr := store.GetBookFiles(fl.file.BookID)
		if gerr != nil {
			updateErrs.Add(1)
			log.Warn("mark-missing-files: load book files", "book", fl.file.BookID, "err", gerr)
			return nil
		}
		var full *database.BookFile
		for i := range siblings {
			if siblings[i].ID == fl.file.ID {
				full = &siblings[i]
				break
			}
		}
		if full == nil {
			updateErrs.Add(1)
			log.Warn("mark-missing-files: row vanished before write", "file", fl.file.ID)
			return nil
		}
		if full.Missing == fl.toValue {
			// Already reconciled (a concurrent run or a re-stat already fixed it).
			return nil
		}
		full.Missing = fl.toValue
		if uerr := store.UpdateBookFile(full.ID, full); uerr != nil {
			updateErrs.Add(1)
			log.Warn("mark-missing-files: update failed", "file", full.ID, "err", uerr)
			return nil
		}
		if fl.toValue {
			markedMissing.Add(1)
		} else {
			clearedStale.Add(1)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Reconciled %d/%d flags (errs=%d)", i+1, t, updateErrs.Load())
		},
	})
	if err != nil {
		return plan, fmt.Errorf("reconcile writes: %w", err)
	}
	plan.MarkedMissing = int(markedMissing.Load())
	plan.ClearedStale = int(clearedStale.Load())
	plan.SkippedChanged = int(skippedChanged.Load())
	plan.UpdateErrs = int(updateErrs.Load())
	if standDownLost.Load() {
		return plan, fmt.Errorf("mark-missing-files: scan stand-down lease lapsed mid-apply after %d flips — aborted (re-run after the scan is idle)", plan.MarkedMissing+plan.ClearedStale)
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
