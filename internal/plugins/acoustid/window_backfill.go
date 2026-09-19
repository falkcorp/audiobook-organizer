// file: internal/plugins/acoustid/window_backfill.go
// version: 1.3.0
// guid: bd9433cb-2459-4d4f-b9cf-4989dfb527de
// last-edited: 2026-09-19

package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// acoustid.window-backfill: the server lane of windowed fingerprints.
//
// Design: .claude/notes/windowed-fingerprint-design-2026-09-12.md, sections
// (b), (c), (e) and (f) PR 4. For every PRESENT tracked book_file it cuts the
// ws1 windows (fingerprint.PlanWindows) through the ffmpeg|fpcalc PCM pipe
// (fingerprint.WindowTools.FileWindow) and stores them in the fpwin: sidecar.
//
// It writes ONLY fpwin: rows (ReplaceFingerprintWindows) and fpwin_fail:
// tombstones (RecordFingerprintWindowFailure). It never writes a book or a
// book_file row, which is why it takes no scan stand-down (design (e)); the
// size/mtime re-check before every write is what protects it from a scan
// rewriting a file underneath it.
//
// Tiers (owner decision 3, "unmatched books first, then present files"):
//
//	T0  present files of books that have at least one tracked book_file row
//	    whose file is missing on disk (statted live, not the Missing flag)
//	T1  every other present file
//
// The design's (e) section listed a further tier ahead of these: untracked
// on-disk candidates from the recover-missing-files report, keyed by path
// (fpwin:p:). Open question (g)2 asked whether to include them; the owner's
// answer was tracked rows only for v1, so that tier is not built here and the
// design's T1/T2 are this op's T0/T1.
//
// Excluded, and counted by reason: rows under the frozen iTunes tree
// (books/itunes/** or the configured iTunes roots; the design is silent on
// reading them, and the standing owner rule is hands off), SkipScan rows,
// non-decodable extensions, empty paths, and files that do not stat as regular
// files.
//
// Dry-run is the default: {"live": true} writes. A dry run builds the full
// plan (stat, stored-window and tombstone reads) and reports it per tier.

// windowBackfillDefID is the op's def ID.
const windowBackfillDefID = "acoustid.window-backfill"

// windowBackfillMaxWorkers caps the execution pool. Every worker runs one
// ffmpeg and one fpcalc at a time.
const windowBackfillMaxWorkers = 32

// windowPlanWorkers sizes the planning pool: a stat and two point reads per
// file, I/O-bound, so it is not tied to the CPU count.
const windowPlanWorkers = 32

// windowPlanChunk is how many rows one planning work item covers. RunItems
// reports progress (a bus event, and an op_log line per distinct label) once
// per item; per-row items would be ~742k of them for microseconds of work
// each on prod.
const windowPlanChunk = 500

// windowCheckpointEvery throttles checkpoints to one per this many completed
// files. A variable so the resume test can checkpoint every file.
var windowCheckpointEvery = 25

var wbLog = logger.New(windowBackfillDefID)

// WindowBackfillParams are the op's parameters. A checkpoint is the same
// struct with Resume set, so a restarted run keeps the caller's settings.
type WindowBackfillParams struct {
	// Live writes windows and tombstones. False (the default) is a dry run.
	Live bool `json:"live,omitempty"`
	// Concurrency overrides the worker count (clamped to 1..32). 0 uses
	// windowWorkers' default.
	Concurrency int `json:"concurrency,omitempty"`
	// Resume is the checkpoint cursor; set by the op, not by callers.
	Resume *WindowBackfillCursor `json:"resume,omitempty"`
}

// WindowBackfillCursor is the resume point: every file of tiers below Tier is
// done, and so is every file of Tier whose ID sorts at or below AfterFileID.
// It is derived from RunItems' contiguous-completion watermark, which is the
// only checkpoint hook RunItems honours at Concurrency > 1 (CheckpointFn is
// called only on the sequential path).
//
// "Done" means handled, not necessarily written: a file that failed
// transiently (stat error, per-window timeout, unknown duration) returns
// normally and moves the watermark past it. A resumed run therefore does not
// retry a transient failure below the cursor; the next run without a cursor
// does (it has neither windows nor a tombstone, so it is planned again).
type WindowBackfillCursor struct {
	Tier        int    `json:"tier"`
	AfterFileID string `json:"after_file_id,omitempty"`
}

// windowTier numbers.
const (
	windowTierUnmatched = 0
	windowTierPresent   = 1
	windowTierCount     = 2
)

var windowTierNames = [windowTierCount]string{"T0 unmatched-book files", "T1 other present files"}

// windowItem is one file that needs windows, reduced to what a worker needs so
// the full BookFileCore slice can be released after planning.
type windowItem struct {
	Tier       int
	FileID     string
	BookID     string
	Path       string
	FpDuration float64
	Duration   float64
	Size       int64
	MtimeUnix  int64
}

// windowWorkers resolves the execution pool size. An explicit parameter wins;
// otherwise FP_PARALLEL_WORKERS (the dial acoustid.backfill and the rescan
// already read, so an operator has one knob for fpcalc pressure); otherwise
// min(NumCPU/2, 16). Always clamped to 1..windowBackfillMaxWorkers.
func windowWorkers(param int) int {
	n := param
	if n <= 0 {
		n = config.AppConfig.FPParallelWorkers
	}
	if n <= 0 {
		n = min(runtime.NumCPU()/2, 16)
	}
	return max(1, min(n, windowBackfillMaxWorkers))
}

// SetToolRegistry wires the server's ToolRegistry so the window op resolves
// fpcalc and ffmpeg through it (both versions are stamped on every window).
func (p *Plugin) SetToolRegistry(r fingerprint.ToolResolver) {
	p.toolsMu.Lock()
	defer p.toolsMu.Unlock()
	p.toolResolver = r
}

// windowTools returns the tools the op runs. Tests override windowToolsFn.
func (p *Plugin) windowTools(ctx context.Context) (fingerprint.WindowTools, error) {
	p.toolsMu.Lock()
	fn, r := p.windowToolsFn, p.toolResolver
	p.toolsMu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	if r == nil {
		return fingerprint.WindowTools{}, errors.New("tool registry not wired into the acoustid plugin (SetToolRegistry)")
	}
	return fingerprint.ResolveWindowTools(ctx, r)
}

func (p *Plugin) windowBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          windowBackfillDefID,
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "acoustid",
		DisplayName: "AcoustID window backfill",
		Description: "Fingerprints 120 s windows at 10/50/90% of every present file (skipping the shared intro) into the fpwin: sidecar. Files in books with missing files go first. Dry-run by default; pass live=true to write.",
		// ResumeRestart re-dispatches with the last checkpoint, which is the
		// caller's params plus the cursor. The plan is rebuilt on resume and
		// already skips every file whose windows are current, so the cursor
		// only narrows the tier being worked; nothing is redone.
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		// Shared with acoustid.backfill, rescan, scan, reset-all, lsh- and
		// duration-backfill (design (e)): never runs beside another
		// fingerprint writer, and so never competes with the head backfill for
		// the same disks. It is NOT library.scan's key and takes no stand-down:
		// it writes no book or book_file row.
		ConcurrencyKey: "acoustid.fingerprint",
		// No Writes declaration: the fpwin: sidecar is not a Book/BookFile
		// resource, and declaring ResBookFiles would serialize this multi-hour
		// read-mostly op against every book_file writer for nothing.
		Timeout: 72 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapFilesRead,
			sdk.CapFilesExecute,
			sdk.CapSubprocessSpawn,
		},
		Run: p.runWindowBackfill,
	}
}

func (p *Plugin) runWindowBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return errors.New("database store not available")
	}
	var params WindowBackfillParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			// Fail closed: a garbled params blob must not silently become a
			// dry run or, worse, lose the Live flag's absence.
			return fmt.Errorf("window-backfill params: %w", err)
		}
	}
	res, err := p.windowBackfill(ctx, reporter, params)
	if err != nil {
		return err
	}
	if res.writeErrors > 0 {
		return fmt.Errorf("window-backfill: %d file(s) could not be written; see log", res.writeErrors)
	}
	return nil
}

// windowPlan is the planning phase's result.
type windowPlan struct {
	tiers       [windowTierCount][]windowItem
	eligible    [windowTierCount]int // present, not excluded
	current     [windowTierCount]int // windows already current
	tombstoned  [windowTierCount]int // durable failure still current
	excluded    map[string]int       // reason -> count
	rows        int
	missingRows int
}

// windowRunResult is what a run did, for tests and the final census.
type windowRunResult struct {
	plan        *windowPlan
	written     int64
	failed      int64 // tombstoned this run
	transient   int64 // not written, not tombstoned; retried by the next run
	stale       int64 // file kept changing under the worker
	rowGone     int64
	writeErrors int64
	skipped     int // cursor-skipped on resume
}

// windowTally is the live counter set; the Label closure runs inside worker
// goroutines, so every field is atomic.
type windowTally struct {
	written, failed, transient, stale, rowGone, writeErrors atomic.Int64
}

func (t *windowTally) summary() string {
	return fmt.Sprintf("written=%d failed=%d transient=%d stale=%d gone=%d write_errors=%d",
		t.written.Load(), t.failed.Load(), t.transient.Load(), t.stale.Load(), t.rowGone.Load(), t.writeErrors.Load())
}

// windowBackfill runs the op body. Split from runWindowBackfill so tests get
// the counts back.
func (p *Plugin) windowBackfill(ctx context.Context, reporter sdk.Reporter, params WindowBackfillParams) (*windowRunResult, error) {
	wt, terr := p.windowTools(ctx)
	if terr != nil {
		if params.Live {
			return nil, fmt.Errorf("window-backfill: %w", terr)
		}
		// A dry run still plans; without tool versions it cannot tell a
		// version-stale window from a current one and says so.
		wbLog.Warn("dry run without tools (%v): tool-version staleness is not evaluated", terr)
	}
	var versions *fingerprint.ToolVersionInfo
	if terr == nil {
		versions = &wt.Versions
	}

	plan, err := p.planWindowBackfill(ctx, reporter, versions)
	if err != nil {
		return nil, err
	}
	res := &windowRunResult{plan: plan}
	logWindowPlan(plan, params.Live)
	if !params.Live {
		_ = reporter.UpdateProgress(1, 1, "Dry run: "+windowPlanSummary(plan))
		return res, nil
	}

	workers := windowWorkers(params.Concurrency)
	p.toolsMu.Lock()
	probe := p.scanProbe
	p.toolsMu.Unlock()
	host, herr := os.Hostname()
	if herr != nil {
		// Host is provenance only; a window without it is still valid.
		wbLog.Warn("hostname: %v; windows will carry an empty host", herr)
		host = ""
	}
	var tally windowTally
	run := &windowRun{p: p, wt: wt, host: host, t: &tally, reporter: reporter, probe: probe}
	total := len(plan.tiers[0]) + len(plan.tiers[1])
	done := 0
	for tier := range windowTierCount {
		items := plan.tiers[tier]
		if c := params.Resume; c != nil {
			switch {
			case tier < c.Tier:
				res.skipped += len(items)
				done += len(items)
				continue
			case tier == c.Tier && c.AfterFileID != "":
				// Items are sorted by file ID; drop the finished prefix.
				cut := sort.Search(len(items), func(i int) bool { return items[i].FileID > c.AfterFileID })
				res.skipped += cut
				done += cut
				items = items[cut:]
			}
		}
		if len(items) == 0 {
			continue
		}
		opts := windowRunOptions(workers, done, total, tier, items, params, reporter, &tally)
		run.progCur.Store(int64(done))
		run.progTotal.Store(int64(total))
		err := registry.RunItems(ctx, reporter, items, func(ctx context.Context, it windowItem) error {
			return run.file(ctx, it)
		}, opts)
		if err != nil {
			fillWindowResult(res, &tally)
			return res, err
		}
		done += len(items)
		// Tier finished: the next resume starts at the next tier.
		next := params
		next.Resume = &WindowBackfillCursor{Tier: tier + 1}
		if cerr := reporter.Checkpoint(next); cerr != nil {
			wbLog.Warn("checkpoint after tier %d: %v", tier, cerr)
		}
	}
	fillWindowResult(res, &tally)

	eligible := plan.eligible[0] + plan.eligible[1]
	current := plan.current[0] + plan.current[1] + int(res.written)
	tombstoned := plan.tombstoned[0] + plan.tombstoned[1] + int(res.failed)
	msg := fmt.Sprintf("Window backfill: %d/%d eligible present files have current windows, %d tombstoned (%s)",
		current, eligible, tombstoned, tally.summary())
	wbLog.Info("%s", msg)
	_ = reporter.UpdateProgress(total, max(total, 1), msg)
	return res, nil
}

func fillWindowResult(res *windowRunResult, t *windowTally) {
	res.written = t.written.Load()
	res.failed = t.failed.Load()
	res.transient = t.transient.Load()
	res.stale = t.stale.Load()
	res.rowGone = t.rowGone.Load()
	res.writeErrors = t.writeErrors.Load()
}

// windowRunOptions builds the RunItems options for one tier. A named function
// so a test runs the real options: an omitted Concurrency is exactly the
// regression that left acoustid.backfill on one core for months.
func windowRunOptions(workers, done, total, tier int, items []windowItem, params WindowBackfillParams,
	reporter sdk.Reporter, tally *windowTally) registry.RunItemsOptions {
	return registry.RunItemsOptions{
		Concurrency:     workers,
		ProgressOffset:  done,
		ProgressTotal:   total,
		CheckpointEvery: windowCheckpointEvery,
		// The watermark is the unbroken completed prefix of items, so
		// items[mark-1] and everything before it are finished even with
		// workers completing out of order. Calls are serialized by RunItems.
		CheckpointStateFn: func(_ context.Context, mark int) error {
			if mark <= 0 || mark > len(items) {
				return nil
			}
			next := params
			next.Resume = &WindowBackfillCursor{Tier: tier, AfterFileID: items[mark-1].FileID}
			if err := reporter.Checkpoint(next); err != nil {
				wbLog.Warn("checkpoint tier %d after %s: %v", tier, items[mark-1].FileID, err)
			}
			return nil
		},
		// Runs inside worker goroutines: reads atomics only.
		Label: func(i, t int) string {
			return fmt.Sprintf("%s: file %d/%d (%s)", windowTierNames[tier], done+i+1, t, tally.summary())
		},
	}
}

// ---- planning ----

// windowPlanRow is one planning decision, written by index so the planning
// pool needs no lock.
type windowPlanRow struct {
	present  bool
	missing  bool
	excluded string // non-empty: reason, not eligible
	state    windowState
	item     windowItem
}

type windowState int

const (
	windowNeedsWork windowState = iota
	windowCurrent
	windowTombstoned
)

func (p *Plugin) planWindowBackfill(ctx context.Context, reporter sdk.Reporter, versions *fingerprint.ToolVersionInfo) (*windowPlan, error) {
	cores, err := p.store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("window-backfill: list book files: %w", err)
	}
	itunes := newITunesMatcher(iTunesRoots())
	rows := make([]windowPlanRow, len(cores))
	var chunks [][2]int
	for lo := 0; lo < len(cores); lo += windowPlanChunk {
		chunks = append(chunks, [2]int{lo, min(lo+windowPlanChunk, len(cores))})
	}
	err = registry.RunItems(ctx, reporter, chunks, func(ctx context.Context, c [2]int) error {
		for i := c[0]; i < c[1]; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			r, perr := p.planOne(cores[i], itunes, versions)
			if perr != nil {
				// A store read failure on one file must not read as "needs
				// work" (it would recompute) or "current" (it would skip);
				// the file is left out of this run and counted.
				wbLog.Warn("plan %s: %v", cores[i].ID, perr)
				r = windowPlanRow{excluded: "plan_error"}
			}
			rows[i] = r // each index written by exactly one chunk
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: windowPlanWorkers,
		Label: func(i, t int) string {
			return fmt.Sprintf("Planning windows: rows %d-%d of %d", chunks[i][0]+1, chunks[i][1], len(cores))
		},
	})
	if err != nil {
		return nil, err
	}

	// Books with at least one missing tracked row are the unmatched books.
	unmatched := make(map[string]bool)
	plan := &windowPlan{excluded: map[string]int{}, rows: len(cores)}
	for i, r := range rows {
		if r.missing {
			plan.missingRows++
			unmatched[cores[i].BookID] = true
		}
	}
	for _, r := range rows {
		if r.excluded != "" {
			plan.excluded[r.excluded]++
			continue
		}
		if !r.present {
			continue
		}
		tier := windowTierPresent
		if unmatched[r.item.BookID] {
			tier = windowTierUnmatched
		}
		plan.eligible[tier]++
		switch r.state {
		case windowCurrent:
			plan.current[tier]++
		case windowTombstoned:
			plan.tombstoned[tier]++
		default:
			it := r.item
			it.Tier = tier
			plan.tiers[tier] = append(plan.tiers[tier], it)
		}
	}
	for t := range windowTierCount {
		slices.SortFunc(plan.tiers[t], func(a, b windowItem) int { return strings.Compare(a.FileID, b.FileID) })
	}
	return plan, nil
}

// planOne classifies one tracked row.
func (p *Plugin) planOne(f database.BookFileCore, itunes *iTunesMatcher, versions *fingerprint.ToolVersionInfo) (windowPlanRow, error) {
	switch {
	case f.FilePath == "":
		return windowPlanRow{excluded: "empty_path"}, nil
	case itunes.under(f.FilePath):
		// Checked before the stat: nothing under the iTunes tree is touched,
		// not even read, and its missing rows do not make a book unmatched.
		return windowPlanRow{excluded: "itunes_tree"}, nil
	}
	fi, err := os.Stat(f.FilePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return windowPlanRow{missing: true}, nil
		}
		// EACCES, EIO, a dead mount: not proof the file is gone, so it
		// neither counts as missing nor gets planned.
		return windowPlanRow{excluded: "stat_error"}, nil
	}
	if !fi.Mode().IsRegular() {
		return windowPlanRow{excluded: "not_regular_file"}, nil
	}
	if f.SkipScan {
		return windowPlanRow{present: true, excluded: "skip_scan"}, nil
	}
	ext := strings.ToLower(filepath.Ext(f.FilePath))
	if !fpcalcDecodableExtensions[ext] {
		return windowPlanRow{present: true, excluded: "non_audio_ext"}, nil
	}
	it := windowItem{
		FileID:     f.ID,
		BookID:     f.BookID,
		Path:       f.FilePath,
		FpDuration: f.AcoustIDFingerprintDurationSec,
		// Pre-CONS-18 rows can hold milliseconds in this seconds field; the
		// write chokepoint repairs only rows that carry FileSize, so judge by
		// the size on disk. A wrong duration moves every window past EOF.
		Duration:  float64(database.NormalizeDurationSec(fi.Size(), f.Duration)),
		Size:      fi.Size(),
		MtimeUnix: fi.ModTime().Unix(),
	}
	state, err := p.windowStateOf(it, versions)
	if err != nil {
		return windowPlanRow{}, err
	}
	return windowPlanRow{present: true, state: state, item: it}, nil
}

// windowStateOf decides whether a file's stored windows (or tombstone) still
// describe the file as it is. Stored rows only: the virtual legacy head from
// WindowsForFile must never look like a current window.
//
// Current means: a non-empty set, every row made by this pipeline and window
// set from these exact bytes (size and mtime), by these tool versions when
// known, and covering the slots the plan would cut today when the duration is
// known from the row. A tombstone is current under the same bytes/pipeline/
// set/tool rule, so a new ffmpeg retries a file the old one could not read.
func (p *Plugin) windowStateOf(it windowItem, versions *fingerprint.ToolVersionInfo) (windowState, error) {
	ref := database.FileWindowRef(it.FileID)
	stored, err := p.store.GetFingerprintWindows(ref)
	if err != nil {
		return windowNeedsWork, err
	}
	if windowsCurrent(stored, it, versions) {
		return windowCurrent, nil
	}
	fail, err := p.store.GetFingerprintWindowFailure(ref)
	if err != nil {
		return windowNeedsWork, err
	}
	if fail != nil && fail.SourceSize == it.Size && fail.SourceMtimeUnix == it.MtimeUnix &&
		fail.Pipeline == fingerprint.WindowPipelineID && fail.WindowSet == fingerprint.WindowSetWS1 &&
		fail.PlannedDurationSec == plannedDuration(it) &&
		(versions == nil || (fail.FpcalcVersion == versions.Fpcalc && fail.FFmpegVersion == versions.FFmpeg)) {
		return windowTombstoned, nil
	}
	return windowNeedsWork, nil
}

func windowsCurrent(stored []database.FingerprintWindow, it windowItem, versions *fingerprint.ToolVersionInfo) bool {
	var have []int
	for _, w := range stored {
		if w.Kind != database.WindowKindWindow {
			continue
		}
		if w.Pipeline != fingerprint.WindowPipelineID || w.WindowSet != fingerprint.WindowSetWS1 ||
			w.SourceSize != it.Size || w.SourceMtimeUnix != it.MtimeUnix {
			return false
		}
		if versions != nil && (w.FpcalcVersion != versions.Fpcalc || w.FFmpegVersion != versions.FFmpeg) {
			return false
		}
		have = append(have, w.SlotBP)
	}
	if len(have) == 0 {
		return false
	}
	// When the row knows the duration, the stored slots must be the ones the
	// plan would cut today. With no stored duration, the windows' own
	// duration (ffprobe at compute time) stands.
	if dur, err := fingerprint.ChooseDuration(it.FpDuration, it.Duration, nil); err == nil {
		specs, perr := fingerprint.PlanWindows(dur, fingerprint.WindowSetWS1)
		if perr != nil {
			return false
		}
		want := make([]int, 0, len(specs))
		for _, s := range specs {
			want = append(want, s.SlotBP)
		}
		slices.Sort(have)
		slices.Sort(want)
		return slices.Equal(have, want)
	}
	return true
}

// iTunesRoots returns the configured iTunes roots (the folder of the library
// file and the media root), cleaned, each also symlink-resolved when it
// resolves to somewhere else. The books/itunes/ segment match in
// config.UnderFrozenITunesTree applies whatever this returns.
func iTunesRoots() []string {
	it := config.AppConfig.ITunes
	var roots []string
	add := func(r string) {
		r = filepath.Clean(r)
		roots = append(roots, r)
		if real, err := filepath.EvalSymlinks(r); err == nil && real != r {
			roots = append(roots, real)
		}
	}
	if it.LibraryReadPath != "" {
		add(filepath.Dir(it.LibraryReadPath))
	}
	if it.MediaRoot != "" {
		add(it.MediaRoot)
	}
	return roots
}

// iTunesMatcher decides whether a path is in the frozen iTunes tree,
// case-insensitively (the tree is reached as "Books/iTunes" on a
// case-insensitive client mount, and HFS/APFS-origin paths vary in case) and
// through symlinks: a library folder that links into the tree is the tree.
//
// Note the lowercasing also excludes a genuinely distinct "Books/Itunes" tree
// on a case-sensitive filesystem. That is deliberate: this is a hands-off
// rule, and the fail-safe direction is to skip.
//
// Symlinks are resolved on the PARENT directory, memoized: planning visits
// every book_file row, EvalSymlinks costs an lstat per path component, and
// files cluster by folder, so one resolve per folder instead of one per row.
// Only a path the cheap textual check did not already exclude is resolved.
type iTunesMatcher struct {
	roots []string // lowercased
	dirs  sync.Map // parent dir -> resolved parent dir ("" when unresolvable)
}

func newITunesMatcher(roots []string) *iTunesMatcher {
	m := &iTunesMatcher{}
	for _, r := range roots {
		m.roots = append(m.roots, strings.ToLower(r))
	}
	return m
}

func (m *iTunesMatcher) textual(p string) bool {
	lp := strings.ToLower(p)
	if config.UnderFrozenITunesTree(lp) {
		return true
	}
	for _, r := range m.roots {
		if pathutil.IsWithin(lp, r) {
			return true
		}
	}
	return false
}

func (m *iTunesMatcher) under(path string) bool {
	if m.textual(path) {
		return true
	}
	dir := filepath.Dir(path)
	var real string
	if v, ok := m.dirs.Load(dir); ok {
		real = v.(string)
	} else {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			real = r
		}
		m.dirs.Store(dir, real)
	}
	if real == "" || real == dir {
		return false
	}
	// A symlinked LEAF inside a real folder is not resolved here; the
	// library's links are folder links, and resolving every leaf is the
	// per-row cost this avoids.
	return m.textual(filepath.Join(real, filepath.Base(path)))
}

func windowPlanSummary(plan *windowPlan) string {
	return fmt.Sprintf("rows=%d missing_rows=%d | T0 eligible=%d current=%d tombstoned=%d todo=%d | T1 eligible=%d current=%d tombstoned=%d todo=%d | excluded=%v",
		plan.rows, plan.missingRows,
		plan.eligible[0], plan.current[0], plan.tombstoned[0], len(plan.tiers[0]),
		plan.eligible[1], plan.current[1], plan.tombstoned[1], len(plan.tiers[1]),
		plan.excluded)
}

func logWindowPlan(plan *windowPlan, live bool) {
	mode := "DRY RUN"
	if live {
		mode = "live"
	}
	wbLog.Info("window-backfill plan (%s): %s", mode, windowPlanSummary(plan))
}

// ---- execution ----

// windowDurableErrors are the FileWindow failures that retrying the same
// bytes with the same tools will not change; each gets a tombstone.
var windowDurableErrors = []error{
	fingerprint.ErrWindowFFmpeg,
	fingerprint.ErrWindowFpcalc,
	fingerprint.ErrWindowParse,
	fingerprint.ErrWindowShortDecode,
	fingerprint.ErrFingerprintTooShort,
}

// windowProbeDuration is the ffprobe fallback of ChooseDuration. A variable
// so tests do not need ffprobe.
var windowProbeDuration = func(ctx context.Context, path string) (float64, error) {
	return audioutil.ProbeDurationSeconds(ctx, "", path)
}

// windowBreakerThreshold is how many I/O-shaped failures in a row (stat
// errors, tool start failures, signal deaths, I/O errors, per-window
// timeouts) stop the run. A dead mount turns every remaining file into one of
// these; without the breaker the op would walk the whole library, write
// nothing, and finish green. A variable so tests can lower it.
var windowBreakerThreshold int64 = 50

// errWindowBreaker is the run's error when the breaker trips.
var errWindowBreaker = errors.New("window-backfill: stopped after too many consecutive I/O failures (is the library mount alive?); rerun once it is: finished files are skipped")

// windowScanPoll is how often a paused worker re-checks for a library.scan.
var windowScanPoll = 30 * time.Second

// windowLogBurst is how many I/O-shaped failures are logged in full before
// the log drops to one line per windowLogEvery.
const (
	windowLogBurst = 20
	windowLogEvery = 100
)

// libraryScanProbe reports whether a library.scan is running.
// *registry.Registry satisfies it (LibraryScanRunning).
type libraryScanProbe interface {
	LibraryScanRunning() bool
}

// setScanProbe wires the registry so the op yields to a library.scan.
func (p *Plugin) setScanProbe(sp libraryScanProbe) {
	p.toolsMu.Lock()
	defer p.toolsMu.Unlock()
	p.scanProbe = sp
}

// windowRun is the state one live run's workers share.
type windowRun struct {
	p        *Plugin
	wt       fingerprint.WindowTools
	host     string
	t        *windowTally
	reporter sdk.Reporter
	probe    libraryScanProbe

	consecutive atomic.Int64 // I/O-shaped failures since the last good file
	ioFailures  atomic.Int64 // all I/O-shaped failures, for log rate-limiting

	progCur, progTotal atomic.Int64 // last progress, re-sent while paused
}

// waitForLibraryScan parks the worker while a library.scan runs (standing
// rule: nothing fights the scanner for the disks, and the scanner may be
// rewriting the very files being cut). It yields rather than taking the scan
// stand-down: a stand-down would park library.scan for this op's whole
// multi-hour run. Progress is re-sent on every poll so the watchdog sees a
// live op, not a wedged one.
func (r *windowRun) waitForLibraryScan(ctx context.Context) error {
	if r.probe == nil || !r.probe.LibraryScanRunning() {
		return nil
	}
	wbLog.Info("library.scan is running; window backfill paused until it finishes")
	for r.probe.LibraryScanRunning() {
		_ = r.reporter.UpdateProgress(int(r.progCur.Load()), int(r.progTotal.Load()), "Paused: library.scan is running")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(windowScanPoll):
		}
	}
	wbLog.Info("library.scan finished; window backfill resuming")
	return nil
}

// good resets the breaker after a file that produced an outcome.
func (r *windowRun) good() { r.consecutive.Store(0) }

// ioFailure tallies an I/O-shaped failure, logs it (rate-limited), and trips
// the breaker at windowBreakerThreshold consecutive ones.
func (r *windowRun) ioFailure(it windowItem, what string, err error) error {
	r.t.transient.Add(1)
	n := r.ioFailures.Add(1)
	if n <= windowLogBurst || n%windowLogEvery == 0 {
		wbLog.Warn("window-backfill %s %s (%s): %v [I/O-shaped failure #%d]", what, it.FileID, it.Path, err, n)
	}
	if c := r.consecutive.Add(1); c >= windowBreakerThreshold {
		return fmt.Errorf("%w: %d in a row, last %s %s: %v", errWindowBreaker, c, what, it.Path, err)
	}
	return nil
}

// plannedDuration is the duration the book_file row gives (0 = unknown). It
// is what a tombstone is pinned to.
func plannedDuration(it windowItem) float64 {
	if d, err := fingerprint.ChooseDuration(it.FpDuration, it.Duration, nil); err == nil {
		return d.Sec
	}
	return 0
}

// file computes and stores one file's windows. It returns an error only when
// the run must stop (cancellation, the breaker, or tools that cannot run at
// all); every other outcome is tallied and returns nil so the watermark moves.
func (r *windowRun) file(ctx context.Context, it windowItem) error {
	if err := r.waitForLibraryScan(ctx); err != nil {
		return err
	}
	// Two passes: a file that changed between planning and the first pass is
	// re-planned once from its new size/mtime; one that changes again while
	// being read is left for the next run.
	for attempt := range 2 {
		fi, err := os.Stat(it.Path)
		if err != nil {
			return r.ioFailure(it, "stat", err)
		}
		it.Size, it.MtimeUnix = fi.Size(), fi.ModTime().Unix()

		prints, dur, slot, werr := computeWindows(ctx, r.wt, it, nil)
		if werr != nil && isDurableWindowErr(werr) && ctx.Err() == nil {
			prints, dur, slot, werr = r.retry(ctx, it, dur, werr)
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if werr != nil && errors.Is(werr, fingerprint.ErrWindowToolsMissing) {
			return werr
		}

		// Re-check the bytes before any write (design (e)).
		after, serr := os.Stat(it.Path)
		if serr != nil {
			return r.ioFailure(it, "stat", serr)
		}
		if after.Size() != it.Size || after.ModTime().Unix() != it.MtimeUnix {
			if attempt == 0 {
				continue
			}
			r.t.stale.Add(1)
			r.good()
			return nil
		}

		switch {
		case werr == nil:
			r.p.storeWindows(it, prints, r.host, r.t)
			r.good()
		case isDurableWindowErr(werr):
			r.p.storeFailure(it, slot, dur, werr, r.wt, r.host, r.t)
			r.good()
		case errors.Is(werr, fingerprint.ErrWindowTransient) || errors.Is(werr, context.DeadlineExceeded):
			return r.ioFailure(it, "cut", werr)
		default:
			// Unknown duration and the like: a fact about this file's row,
			// not about the mount. Retried next run; does not feed the breaker.
			wbLog.Debug("window %s: transient: %v", it.FileID, werr)
			r.t.transient.Add(1)
		}
		return nil
	}
	return nil
}

// retry is the single inline retry before a durable tombstone. A short
// decode or too few frames usually means the duration was wrong (a
// size/bitrate estimate, a stale row), so the file is ffprobed and re-planned
// from what ffprobe says. Anything else is retried as-is. Only the SAME
// deterministic failure on the retry is returned as durable; a different
// outcome is marked transient.
func (r *windowRun) retry(ctx context.Context, it windowItem, dur fingerprint.DurationUsed, first error) ([]*fingerprint.WindowPrint, fingerprint.DurationUsed, int, error) {
	var forced *fingerprint.DurationUsed
	if (errors.Is(first, fingerprint.ErrWindowShortDecode) || errors.Is(first, fingerprint.ErrFingerprintTooShort)) &&
		dur.Source != fingerprint.DurationSourceFFprobe {
		if sec, perr := windowProbeDuration(ctx, it.Path); perr == nil && sec > 0 {
			forced = &fingerprint.DurationUsed{Sec: sec, Source: fingerprint.DurationSourceFFprobe}
		}
	}
	prints, dur2, slot, err := computeWindows(ctx, r.wt, it, forced)
	switch {
	case err == nil:
		return prints, dur2, slot, nil
	case isDurableWindowErr(err) && windowFailReason(err) == windowFailReason(first):
		return nil, dur2, slot, err
	default:
		return nil, dur2, slot, fmt.Errorf("%w: retry disagreed (first: %v; retry: %w)", fingerprint.ErrWindowTransient, first, err)
	}
}

// computeWindows plans and cuts every window of one file, one after another
// on this worker (so an m4b's moov read stays in the page cache). All or
// nothing: the first failing window fails the file and its slot is returned.
// forced, when set, replaces the row's duration (the post-ffprobe retry).
func computeWindows(ctx context.Context, wt fingerprint.WindowTools, it windowItem, forced *fingerprint.DurationUsed) ([]*fingerprint.WindowPrint, fingerprint.DurationUsed, int, error) {
	var dur fingerprint.DurationUsed
	if forced != nil {
		dur = *forced
	} else {
		d, err := fingerprint.ChooseDuration(it.FpDuration, it.Duration, func() (float64, error) {
			return windowProbeDuration(ctx, it.Path)
		})
		if err != nil {
			return nil, dur, 0, err
		}
		dur = d
	}
	specs, err := fingerprint.PlanWindows(dur, fingerprint.WindowSetWS1)
	if err != nil {
		return nil, dur, 0, err
	}
	out := make([]*fingerprint.WindowPrint, 0, len(specs))
	for _, s := range specs {
		wp, err := wt.FileWindow(ctx, it.Path, s)
		if err != nil {
			return nil, dur, s.SlotBP, err
		}
		out = append(out, wp)
	}
	return out, dur, 0, nil
}

// isDurableWindowErr: a pipeline failure that says something about the file.
// ErrWindowTransient is checked first because it is wrapped alongside
// ErrWindowFFmpeg/ErrWindowFpcalc.
func isDurableWindowErr(err error) bool {
	if errors.Is(err, fingerprint.ErrWindowTransient) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	for _, d := range windowDurableErrors {
		if errors.Is(err, d) {
			return true
		}
	}
	return false
}

func (p *Plugin) storeWindows(it windowItem, prints []*fingerprint.WindowPrint, host string, t *windowTally) {
	ref := database.FileWindowRef(it.FileID)
	now := time.Now().UTC()
	rows := make([]database.FingerprintWindow, 0, len(prints))
	for _, wp := range prints {
		rows = append(rows, database.FingerprintWindow{
			SchemaVersion:   database.FingerprintWindowSchemaVersion,
			Ref:             ref,
			Kind:            database.FingerprintWindowKind(wp.Kind),
			SlotBP:          wp.SlotBP,
			WindowSet:       wp.WindowSet,
			OffsetSec:       wp.OffsetSec,
			LengthSec:       wp.LengthSec,
			DecodedSec:      wp.DecodedSec,
			CoversWhole:     wp.CoversWhole,
			DurationUsedSec: wp.DurationUsedSec,
			DurationSource:  string(wp.DurationSource),
			Frames:          wp.Frames,
			Raw:             wp.Raw,
			Algorithm:       wp.Algorithm,
			Pipeline:        wp.Pipeline,
			FpcalcVersion:   wp.FpcalcVersion,
			FFmpegVersion:   wp.FFmpegVersion,
			SourceSize:      it.Size,
			SourceMtimeUnix: it.MtimeUnix,
			ComputedAt:      now,
			Host:            host,
		})
	}
	if err := p.store.ReplaceFingerprintWindows(ref, rows); err != nil {
		p.countWriteErr(it, err, t)
		return
	}
	t.written.Add(1)
}

func (p *Plugin) storeFailure(it windowItem, slot int, dur fingerprint.DurationUsed, werr error, wt fingerprint.WindowTools, host string, t *windowTally) {
	detail := werr.Error()
	if len(detail) > 1024 {
		detail = detail[:1024]
	}
	f := &database.FingerprintWindowFailure{
		SchemaVersion:      database.FingerprintWindowSchemaVersion,
		Ref:                database.FileWindowRef(it.FileID),
		Reason:             windowFailReason(werr),
		Detail:             detail,
		SlotBP:             slot,
		WindowSet:          fingerprint.WindowSetWS1,
		Pipeline:           fingerprint.WindowPipelineID,
		FpcalcVersion:      wt.Versions.Fpcalc,
		FFmpegVersion:      wt.Versions.FFmpeg,
		SourceSize:         it.Size,
		SourceMtimeUnix:    it.MtimeUnix,
		DurationUsedSec:    dur.Sec,
		DurationSource:     string(dur.Source),
		PlannedDurationSec: plannedDuration(it),
		FailedAt:           time.Now().UTC(),
		Host:               host,
	}
	if err := p.store.RecordFingerprintWindowFailure(f); err != nil {
		p.countWriteErr(it, err, t)
		return
	}
	t.failed.Add(1)
}

// countWriteErr separates a row deleted mid-run from a real write failure,
// which fails the run at the end rather than passing as success. The store
// resolves the row by FILE ID and says so with ErrFingerprintWindowRowGone, so
// a row that moved to another book is never mistaken for a gone one.
func (p *Plugin) countWriteErr(it windowItem, err error, t *windowTally) {
	if errors.Is(err, database.ErrFingerprintWindowRowGone) {
		t.rowGone.Add(1)
		return
	}
	wbLog.Error("window-backfill write %s: %v", it.FileID, err)
	t.writeErrors.Add(1)
}

func windowFailReason(err error) string {
	switch {
	case errors.Is(err, fingerprint.ErrWindowFFmpeg):
		return "ffmpeg"
	case errors.Is(err, fingerprint.ErrWindowFpcalc):
		return "fpcalc"
	case errors.Is(err, fingerprint.ErrWindowParse):
		return "parse"
	case errors.Is(err, fingerprint.ErrWindowShortDecode):
		return "short_decode"
	case errors.Is(err, fingerprint.ErrFingerprintTooShort):
		return "too_few_frames"
	}
	return "other"
}
