// file: internal/plugins/maintenance/file_intro_transcribe.go
// version: 1.0.3
// guid: 2a9d6f41-c803-4be7-95a0-7d14e8b3c6f2
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// maintenance.transcribe-file-intros transcribes the first fileIntroClipSeconds
// of EVERY present audio file (book_file row), not only a book's first. It
// differs from maintenance.transcribe-book-intros in what it stores and where:
//
//   - Results go on the book_file row (the group documented on
//     database.BookFile), keyed by the file's FileHash, so two files with the
//     same bytes are transcribed once and a rescan keeps the result.
//   - It never decodes audio on the server. Each file's path and the clip
//     length go to the whisper worker (see file_intro_transport.go).
//   - It FILLS an empty book transcript from a book's ordinal-0 file, and
//     never overwrites one.
//
// A file is DONE when IntroSourceHash == FileHash; that marker is on
// BookFileCore (memdb), so selecting the remaining work is one in-memory pass
// and a restart resumes exactly where it stopped, with no checkpoint (the
// checkpoint hook is not called by RunItems under concurrency anyway).

const (
	fileIntroOpID = "maintenance.transcribe-file-intros"

	// fileIntroBatchGroups is how many distinct audio files one worker call
	// carries: small enough that a transport outage defers little work and a
	// result lands within seconds, large enough to amortise the request.
	fileIntroBatchGroups = 8

	// fileIntroEstimateSecPerFile is the design's figure for one slot,
	// measured for the book op's local-clip path. It is NOT measured for
	// worker-side extraction over the share, and the preview says so.
	fileIntroEstimateSecPerFile = 5.3

	fileIntroSamples = 5
)

// fileIntroPauseInterval is how often a paused run re-checks the queue. A var
// so tests do not wait 30 s.
var fileIntroPauseInterval = 30 * time.Second

// fileIntroPauseMaxListErrors is how many consecutive queue-listing failures a
// paused run tolerates before it gives up. Failing open (running through a
// scan) is the one wrong answer, so it stops instead.
const fileIntroPauseMaxListErrors = 10

// fileIntroBusyOps are the operations the run pauses for: both rewrite
// book_file rows in bulk. The scheduler cannot do this for us (see the Writes
// note on the def), so the run polls the queue between batches instead.
var fileIntroBusyOps = map[string]bool{
	"library.scan":                true,
	"metadata.batch-apply-cached": true,
}

type fileIntroParams struct {
	// OnlyMissing (default true) leaves files that already carry a result
	// alone and never overwrites one. False re-transcribes every eligible
	// file and replaces what is there; clearing such a run is roll-forward
	// only (see ClearRunID).
	OnlyMissing *bool `json:"only_missing,omitempty"`
	// RetryFailed re-attempts files whose last attempt failed for a durable
	// reason. source_file_missing heals by itself when the file reappears.
	RetryFailed *bool `json:"retry_failed,omitempty"`
	// ClearRunID switches the op to rollback: it removes the per-file results
	// the named run wrote, and the book-level fills that came from them.
	ClearRunID string `json:"clear_run_id,omitempty"`
	// DryRun omitted means PREVIEW. Both spellings are resolved together by
	// opmode.ResolveDryRun.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

func (p *Plugin) fileIntroTranscribeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              fileIntroOpID,
		Preview:         sdk.PreviewSpec{Params: fileIntroParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "Transcribe file intros",
		Description:     "Transcribes the first 90 seconds of every present audio file, not just each book's first, and stores the result on the file's own record (keyed by the file's hash, so identical files are transcribed once and a rescan keeps the result). The server does no audio decoding: each file's path goes to the whisper worker, which reads and cuts the clip itself. A book's first file is handled first, and its result fills the book's transcript only when that is empty; an existing book transcript is never overwritten. Resumable: a file is done once its stored marker matches its hash, so a restart or a repeat run does only what is left. The run pauses while a library scan or a bulk metadata apply is running. dry_run defaults to true: a run that states no mode only counts the work and estimates the time. Param clear_run_id=<op id> removes what that run wrote (dry run first for the count); it returns files to untranscribed and cannot restore a transcript a forced re-run (only_missing=false) replaced.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  fileIntroOpID,
		// NO Writes declaration, on purpose. Gate 3b of the dispatcher holds any op
		// back while a running op declares an overlapping write-set, and this op
		// runs for days: declaring ResBooks/ResBookFiles would keep the nightly
		// acoustid.backfill (and every other op that declares them) queued for the
		// whole run. The gate exists to stop whole-row read-modify-write ops from
		// interleaving, and neither side of that applies here: this op writes only
		// the intro group of one row (SetBookFileIntro, under the row's lock) and
		// fills a book through ModifyBook (under the book's lock), and a whole-row
		// writer cannot revert the intro group because the shared book_file merge
		// classes keep it (bookFileFieldClasses). It still writes, so it declares
		// the write capabilities, which is what makes v3 treat it as a writer.
		// The one thing it does not cover is a whole-row book writer losing a
		// book fill that landed between its read and its write; the file result
		// survives and a forced run or clear_run_id puts that right.
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * 24 * time.Hour,
		ProgressTimeout: 30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapFilesRead},
		Run:             p.runFileIntroTranscribe,
	}
}

// fileIntroItem is one eligible file with the facts selection used.
type fileIntroItem struct {
	BookID  string
	FileID  string
	Path    string
	Ordinal int // 0 = the book's first audio file
}

// fileIntroGroup is every selected file sharing one FileHash: one whisper job
// (or one donor copy) whose result is applied to all of them.
type fileIntroGroup struct {
	Hash    string
	Members []fileIntroItem
	// Donor is a file already done with this hash whose stored result is
	// copied instead of transcribing. Zero when none.
	DonorBookID, DonorFileID string
}

func (g *fileIntroGroup) tier() int {
	for _, m := range g.Members {
		if m.Ordinal == 0 {
			return 0
		}
	}
	return 1
}

// fileIntroSelection is the outcome of reading the library once.
type fileIntroSelection struct {
	groups []fileIntroGroup

	eligible           int
	transcribedAtStart int
	selected           int // files in groups
	reuseFiles         int // of selected: files whose group has a donor
	skippedNoHash      int
	skippedMissing     int
	skippedUnsupported int
	skippedFailed      int
	samples            []string
}

type fileIntroSelectOpts struct {
	onlyMissing bool
	retryFailed bool
	// stat reports whether a path exists; a seam so selection is testable
	// without files. nil means os.Stat.
	stat func(string) bool
}

func isTranscribable(f *database.BookFileCore) bool {
	return f.FilePath != "" && transcribableExtSet[strings.ToLower(filepath.Ext(f.FilePath))]
}

func fileHasResult(f *database.BookFileCore) bool {
	if f.FileHash != "" && f.IntroSourceHash == f.FileHash {
		return true
	}
	// A result written before the marker existed (the book op's migration, an
	// earlier apply): trusted under only_missing, never used as a donor.
	if f.IntroTranscribedAt != nil && f.TranscribeStatus != nil &&
		(*f.TranscribeStatus == statusOK || *f.TranscribeStatus == statusUnparsed) {
		return true
	}
	return false
}

// selectFileIntroWork reads the whole library's book_file projection once and
// decides, per file, whether it is work. Pure over its inputs apart from the
// stat seam; no store writes.
func selectFileIntroWork(cores []database.BookFileCore, opts fileIntroSelectOpts) fileIntroSelection {
	stat := opts.stat
	if stat == nil {
		stat = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	var sel fileIntroSelection

	byBook := make(map[string][]*database.BookFileCore)
	for i := range cores {
		f := &cores[i]
		if !isTranscribable(f) {
			sel.skippedUnsupported++
			continue
		}
		byBook[f.BookID] = append(byBook[f.BookID], f)
	}

	// Donors: a file done with its hash marker, one per hash.
	donors := make(map[string]*database.BookFileCore)
	for i := range cores {
		f := &cores[i]
		if f.FileHash != "" && f.IntroSourceHash == f.FileHash && isTranscribable(f) {
			if _, ok := donors[f.FileHash]; !ok {
				donors[f.FileHash] = f
			}
		}
	}

	byHash := make(map[string]*fileIntroGroup)
	var order []string
	for bookID, files := range byBook {
		// The book op's ordering: disc, track, path. Ordinal 0 is therefore the
		// same file it calls "the first". Missing rows count for ordering, so
		// a missing first track does not promote track 2 to ordinal 0.
		sort.Slice(files, func(i, j int) bool {
			if files[i].DiscNumber != files[j].DiscNumber {
				return files[i].DiscNumber < files[j].DiscNumber
			}
			if files[i].TrackNumber != files[j].TrackNumber {
				return files[i].TrackNumber < files[j].TrackNumber
			}
			return files[i].FilePath < files[j].FilePath
		})
		for ord, f := range files {
			switch {
			case f.Missing:
				sel.skippedMissing++
				continue
			case f.FileHash == "":
				sel.skippedNoHash++
				continue
			}
			sel.eligible++
			done := fileHasResult(f)
			if done {
				sel.transcribedAtStart++
				if opts.onlyMissing {
					continue
				}
			}
			if !done && f.TranscribeStatus != nil && isDurableTranscribeFailure(*f.TranscribeStatus) && !opts.retryFailed {
				// source_file_missing heals the moment the file is back.
				if *f.TranscribeStatus != statusSourceMissing || !stat(f.FilePath) {
					sel.skippedFailed++
					continue
				}
			}
			g := byHash[f.FileHash]
			if g == nil {
				g = &fileIntroGroup{Hash: f.FileHash}
				if d, ok := donors[f.FileHash]; ok && opts.onlyMissing && d.ID != f.ID {
					g.DonorBookID, g.DonorFileID = d.BookID, d.ID
				}
				byHash[f.FileHash] = g
				order = append(order, f.FileHash)
			}
			g.Members = append(g.Members, fileIntroItem{
				BookID: bookID, FileID: f.ID, Path: f.FilePath, Ordinal: ord,
			})
			sel.selected++
			if g.DonorFileID != "" {
				sel.reuseFiles++
			}
			if len(sel.samples) < fileIntroSamples {
				sel.samples = append(sel.samples, f.ID)
			}
		}
	}

	groups := make([]fileIntroGroup, 0, len(order))
	for _, h := range order {
		g := byHash[h]
		sort.Slice(g.Members, func(i, j int) bool {
			a, b := g.Members[i], g.Members[j]
			if a.Ordinal != b.Ordinal {
				return a.Ordinal < b.Ordinal
			}
			if a.BookID != b.BookID {
				return a.BookID < b.BookID
			}
			return a.FileID < b.FileID
		})
		groups = append(groups, *g)
	}
	// Every book's opening is worth most (it carries the credits and fills the
	// book transcript), so all ordinal-0 groups run before any other file;
	// within a tier the order is by first member, which is stable run to run.
	sort.SliceStable(groups, func(i, j int) bool {
		ti, tj := groups[i].tier(), groups[j].tier()
		if ti != tj {
			return ti < tj
		}
		a, b := groups[i].Members[0], groups[j].Members[0]
		if a.BookID != b.BookID {
			return a.BookID < b.BookID
		}
		return a.Ordinal < b.Ordinal
	})
	sel.groups = groups
	return sel
}

// fileIntroAccum is the run's counters, mutex-guarded because batches finish
// concurrently, and the sink that persists them.
type fileIntroAccum struct {
	mu    sync.Mutex
	stats database.FileIntroStats
	sink  database.FileIntroStatsStore
	log   *slog.Logger // reporter's logger; nil in tests that do not care
}

func (a *fileIntroAccum) update(fn func(s *database.FileIntroStats)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn(&a.stats)
}

func (a *fileIntroAccum) snapshot() database.FileIntroStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

// flush persists the counters; best-effort, as the book op's is.
func (a *fileIntroAccum) flush(done bool) {
	if a.sink == nil {
		return
	}
	a.mu.Lock()
	a.stats.UpdatedAt = time.Now()
	a.stats.Done = done
	cp := a.stats
	a.mu.Unlock()
	if err := a.sink.PutFileIntroStats(&cp); err != nil && a.log != nil {
		a.log.Warn("transcribe-file-intros: stats write failed", "err", err)
	}
}

func fileIntroRunID(reporter sdk.Reporter) string {
	if id := registry.ReporterOpID(reporter); id != "" {
		return id
	}
	return "run-" + time.Now().UTC().Format(time.RFC3339)
}

// fileIntroStore is the slice of the ops store this op reads and writes
// through ordinary methods. (The field-scoped book_file writes come through
// database.BookFileIntroStore, a capability.)
type fileIntroStore interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetBookByID(id string) (*database.Book, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

func (p *Plugin) runFileIntroTranscribe(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	var params fileIntroParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("%s: decode params: %w", fileIntroOpID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(fileIntroOpID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	if params.ClearRunID != "" {
		return p.clearFileIntros(ctx, store, reporter, params.ClearRunID, dryRun)
	}

	onlyMissing := params.OnlyMissing == nil || *params.OnlyMissing
	retryFailed := params.RetryFailed != nil && *params.RetryFailed
	mark := introModeMarker(dryRun)
	log := reporter.Logger()

	_ = reporter.UpdateProgress(0, 1, mark+"Selecting work — reading every book file")
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("%s: read book files: %w", fileIntroOpID, err)
	}
	sel := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: onlyMissing, retryFailed: retryFailed})
	cores = nil

	if dryRun {
		return previewFileIntros(reporter, sel, onlyMissing)
	}

	slots := fileIntroSlots()
	if slots < 1 {
		return fmt.Errorf("%s: no whisper endpoint is configured (whisper_endpoints / whisper_remote_url); refusing to start a run that cannot transcribe", fileIntroOpID)
	}
	if err := fileIntroPreflight(ctx); err != nil {
		return err
	}
	intros, ok := database.AsCapability[database.BookFileIntroStore](store)
	if !ok {
		return fmt.Errorf("%s: the store does not support per-file intro writes", fileIntroOpID)
	}
	queue := p.deps.OperationQueueStore()

	runID := fileIntroRunID(reporter)
	startedAt := time.Now()
	sink, _ := database.AsCapability[database.FileIntroStatsStore](store)
	accum := &fileIntroAccum{sink: sink, log: log, stats: database.FileIntroStats{
		RunOpID: runID, StartedAt: startedAt, UpdatedAt: startedAt,
		Eligible: sel.eligible, TranscribedAtStart: sel.transcribedAtStart, TranscribedNow: sel.transcribedAtStart,
		Selected:      sel.selected,
		SkippedNoHash: sel.skippedNoHash, SkippedMissing: sel.skippedMissing,
		SkippedUnsupported: sel.skippedUnsupported, SkippedFailed: sel.skippedFailed,
	}}
	log.Info("transcribe-file-intros: starting",
		"run_id", runID, "only_missing", onlyMissing, "retry_failed", retryFailed,
		"eligible", sel.eligible, "already_done", sel.transcribedAtStart,
		"selected", sel.selected, "groups", len(sel.groups), "reuse_files", sel.reuseFiles, "slots", slots)

	if len(sel.groups) == 0 {
		accum.flush(true)
		_ = reporter.UpdateProgress(1, 1, mark+fmt.Sprintf(
			"Done — nothing to transcribe (%d of %d eligible files already have a result)",
			sel.transcribedAtStart, sel.eligible))
		return nil
	}
	accum.flush(false)

	w := &fileIntroWorker{
		store: store, intros: intros, queue: queue, reporter: reporter, accum: accum,
		runID: runID, onlyMissing: onlyMissing,
	}
	batches := chunkFileIntroGroups(sel.groups, fileIntroBatchGroups)
	w.batchesTotal = len(batches)
	var filesDone atomic.Int64
	err = registry.RunItems(ctx, reporter, batches, func(ctx context.Context, b []fileIntroGroup) error {
		n, berr := w.runBatch(ctx, b)
		w.batchesDone.Add(1)
		filesDone.Add(int64(n))
		accum.flush(false)
		return berr
	}, registry.RunItemsOptions{
		// One worker per whisper slot: the transport enforces each endpoint's
		// own cap, this only keeps the pool fed without queueing far past it.
		Concurrency:   slots,
		ProgressTotal: len(batches),
		Label: func(i, t int) string {
			return mark + fmt.Sprintf("Batch %d/%d — %d of %d files handled", i+1, t, int(filesDone.Load()), sel.selected)
		},
	})
	accum.flush(true)
	st := accum.snapshot()
	log.Info("transcribe-file-intros: finished",
		"run_id", runID, "attempted", st.Attempted, "ok", st.OK, "unparsed", st.Unparsed, "silence", st.Silence,
		"whisper_error", st.WhisperError, "ffmpeg_error", st.FFmpegError, "source_missing", st.SourceMissing, "reused", st.Reused,
		"deferred", st.Deferred, "stale", st.Stale, "already_had", st.AlreadyHad, "book_fills", st.BookFills,
		"coverage_now", st.TranscribedNow, "eligible", st.Eligible, "err", err)
	if err != nil {
		return err
	}
	_ = reporter.UpdateProgress(1, 1, mark+fmt.Sprintf(
		"Done — run %s: %d ok, %d unparsed, %d silent, %d whisper-err, %d ffmpeg-err, %d source-missing, %d reused, %d deferred, %d stale; %d book transcripts filled; coverage %d/%d files",
		runID, st.OK, st.Unparsed, st.Silence, st.WhisperError, st.FFmpegError, st.SourceMissing, st.Reused, st.Deferred, st.Stale,
		st.BookFills, st.TranscribedNow, st.Eligible))
	return nil
}

func chunkFileIntroGroups(gs []fileIntroGroup, size int) [][]fileIntroGroup {
	out := make([][]fileIntroGroup, 0, (len(gs)+size-1)/size)
	for i := 0; i < len(gs); i += size {
		out = append(out, gs[i:min(i+size, len(gs))])
	}
	return out
}

// previewFileIntros reports what a live run would do. It writes nothing:
// no stats key, no row, no whisper call.
func previewFileIntros(reporter sdk.Reporter, sel fileIntroSelection, onlyMissing bool) error {
	slots := max(fileIntroSlots(), 1)
	toSend := len(sel.groups)
	for _, g := range sel.groups {
		if g.DonorFileID != "" {
			toSend--
		}
	}
	est := time.Duration(float64(toSend)*fileIntroEstimateSecPerFile/float64(slots)) * time.Second
	msg := introPreviewMarker + fmt.Sprintf(
		"would handle %d files (%d distinct audio files to send to whisper, %d files copied from an identical done file); "+
			"coverage now %d of %d eligible files; skipped: %d without a hash, %d missing, %d unsupported format, %d with a durable failure (retry_failed=true re-attempts); "+
			"estimate %s at %.1f s per file over %d whisper slot(s) — the per-file time is UNMEASURED for worker-side extraction; first file ids: [%s]. Pass dry_run=false to run.",
		sel.selected, toSend, sel.reuseFiles, sel.transcribedAtStart, sel.eligible,
		sel.skippedNoHash, sel.skippedMissing, sel.skippedUnsupported, sel.skippedFailed,
		est.Round(time.Minute), fileIntroEstimateSecPerFile, slots, strings.Join(sel.samples, ", "))
	reporter.Logger().Info("transcribe-file-intros: preview",
		"only_missing", onlyMissing, "selected_files", sel.selected, "groups", len(sel.groups),
		"reuse_files", sel.reuseFiles, "eligible", sel.eligible, "already_done", sel.transcribedAtStart,
		"skipped_no_hash", sel.skippedNoHash, "skipped_missing", sel.skippedMissing,
		"skipped_unsupported", sel.skippedUnsupported, "skipped_failed", sel.skippedFailed,
		"slots", slots, "estimate", est.String())
	_ = reporter.UpdateProgress(1, 1, msg)
	return nil
}

// fileIntroWorker holds what a batch needs; shared by every worker goroutine,
// so everything on it is read-only or internally synchronised.
type fileIntroWorker struct {
	store       fileIntroStore
	intros      database.BookFileIntroStore
	queue       OpQueueReader
	reporter    sdk.Reporter
	accum       *fileIntroAccum
	runID       string
	onlyMissing bool
	// batchesDone / batchesTotal let a paused run re-announce itself at its real
	// position instead of resetting the progress bar to zero on every tick.
	batchesDone  atomic.Int64
	batchesTotal int
	// progressMu serialises reporter calls made outside RunItems' own: the
	// reporter is not required to be goroutine-safe.
	progressMu sync.Mutex
}

func (w *fileIntroWorker) progress(msg string) {
	w.progressMu.Lock()
	defer w.progressMu.Unlock()
	_ = w.reporter.UpdateProgress(int(w.batchesDone.Load()), max(w.batchesTotal, 1), msg)
}

// waitWhileBusy blocks while a library scan or a bulk metadata apply is
// queued or running. It selects on ctx.Done in every wait, re-announces itself
// so the watchdog sees the op as alive, and fails (never proceeds) when the
// queue cannot be read repeatedly.
func (w *fileIntroWorker) waitWhileBusy(ctx context.Context) error {
	if w.queue == nil {
		return fmt.Errorf("%s: cannot verify no library scan is active; refusing to run", fileIntroOpID)
	}
	listErrs := 0
	for {
		active, err := w.queue.ListActiveOperationsV2()
		if err != nil {
			listErrs++
			if listErrs >= fileIntroPauseMaxListErrors {
				return fmt.Errorf("%s: cannot list active operations (%d tries): %w", fileIntroOpID, listErrs, err)
			}
		} else {
			listErrs = 0
			busy := ""
			for _, op := range active {
				if fileIntroBusyOps[op.DefID] && (op.Status == "running" || op.Status == "queued") {
					busy = op.DefID
					break
				}
			}
			if busy == "" {
				return nil
			}
			w.progress(introLiveMarker + "paused — " + busy + " is running; resumes when it finishes")
		}
		t := time.NewTimer(fileIntroPauseInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// groupResult is what a group's file result looks like before it is applied
// to each member.
type groupResult struct {
	res       database.BookFileIntroResult // SourceHash and RunOpID filled by the caller per member
	kind      string                       // "ok" "unparsed" "silence" "whisper_error" "source_missing" "deferred" "reuse"
	fillable  bool                         // a real transcript (not the silence sentinel, not a failure)
	reuse     bool
	hasResult bool
}

// runBatch handles one batch of groups and returns how many files it settled.
func (w *fileIntroWorker) runBatch(ctx context.Context, groups []fileIntroGroup) (int, error) {
	if err := w.waitWhileBusy(ctx); err != nil {
		return 0, err
	}
	log := w.reporter.Logger()
	settled := 0

	results := make([]groupResult, len(groups))
	jobs := make(map[string]fileIntroJob)
	jobGroup := make(map[string]int) // job key -> group index
	for gi, g := range groups {
		if g.DonorFileID != "" {
			results[gi] = w.donorResult(g)
			if results[gi].hasResult {
				continue
			}
			// Donor unreadable: fall through and transcribe it afresh.
		}
		path := ""
		for _, m := range g.Members {
			if _, err := os.Stat(m.Path); err == nil {
				path = m.Path
				break
			}
		}
		if path == "" {
			results[gi] = groupResult{kind: "source_missing", hasResult: true, res: database.BookFileIntroResult{
				Status: statusSourceMissing, Error: "source file not found: " + g.Members[0].Path,
			}}
			continue
		}
		key := g.Members[0].FileID
		jobs[key] = fileIntroJob{Path: path, Seconds: fileIntroClipSeconds}
		jobGroup[key] = gi
	}

	if len(jobs) > 0 {
		out, err := fileIntroWhisperFn(ctx, jobs, nil)
		var te *transcribe.TransportError
		switch {
		case errors.As(err, &te):
			// Endpoint problem, not a file problem: write NOTHING, the files
			// stay undone and the next run picks them up.
			log.Warn("transcribe-file-intros: endpoints unreachable, batch deferred (nothing written)",
				"jobs", len(jobs), "endpoints", te.Endpoints, "err", te.Err)
			for _, gi := range jobGroup {
				results[gi] = groupResult{kind: "deferred", hasResult: true}
			}
		case ctx.Err() != nil:
			return 0, ctx.Err()
		case err != nil:
			log.Warn("transcribe-file-intros: whisper batch failed", "jobs", len(jobs), "err", err)
			detail := truncateDetail(err.Error())
			for _, gi := range jobGroup {
				results[gi] = groupResult{kind: "whisper_error", hasResult: true, res: database.BookFileIntroResult{
					Status: statusWhisperError, Error: detail,
				}}
			}
		default:
			now := time.Now()
			for key, gi := range jobGroup {
				results[gi] = whisperGroupResult(out[key], now)
			}
		}
	}

	for gi, g := range groups {
		gr := results[gi]
		n, err := w.applyGroup(g, gr)
		settled += n
		if err != nil {
			return settled, err
		}
	}
	return settled, nil
}

// whisperGroupResult turns the worker's answer into the result every member of
// the group gets.
func whisperGroupResult(r fileIntroWhisperResult, now time.Time) groupResult {
	switch {
	case r.Error != "":
		// The worker prefixes a per-file error with a code (transcribe.PathErr*).
		// The code decides how durable the verdict is:
		//   not_found    -> the file is absent: source_file_missing, which heals
		//                   by itself when it reappears;
		//   ffmpeg_error -> the worker could not decode it: a verdict about the
		//                   file, held until retry_failed (so a corrupt file is
		//                   not re-sent by every run);
		//   path_unmapped, ffmpeg_timeout -> the mount or the share, not the
		//                   file: written as nothing, the file stays undone;
		//   anything else (whisper_error, bad_request, no code) -> whisper_error,
		//                   retried by the next run.
		detail := truncateDetail(r.Error)
		switch transcribe.PathErrorCode(r.Error) {
		case transcribe.PathErrNotFound:
			return groupResult{kind: "source_missing", hasResult: true, res: database.BookFileIntroResult{
				Status: statusSourceMissing, Error: detail, At: now}}
		case transcribe.PathErrFFmpeg:
			return groupResult{kind: "ffmpeg_error", hasResult: true, res: database.BookFileIntroResult{
				Status: statusFFmpegError, Error: detail, At: now}}
		case transcribe.PathErrUnmapped, transcribe.PathErrFFmpegTimeout:
			return groupResult{kind: "deferred", hasResult: true}
		}
		return groupResult{kind: "whisper_error", hasResult: true, res: database.BookFileIntroResult{
			Status: statusWhisperError, Error: detail, At: now,
		}}
	case strings.TrimSpace(r.Text) == "":
		// Nothing to transcribe at the clip length. The sentinel makes the
		// file done (re-running cannot improve it); it is a FILE marker only
		// and never fills a book (see fileIntroFillBook).
		s := transcribe.SilenceSentinel
		return groupResult{kind: "silence", hasResult: true, res: database.BookFileIntroResult{
			Status: statusEmpty, Error: "whisper returned empty text", Transcript: &s, At: now, Done: true,
			Meta: &database.IntroMeta{Model: r.Model, Language: r.Language, ClipLengthSec: fileIntroClipSeconds},
		}}
	default:
		fields := transcribe.ParseAudiobookIntro(r.Text)
		status, kind := statusOK, "ok"
		if fields.Title == "" {
			status, kind = statusUnparsed, "unparsed"
		}
		text := r.Text
		return groupResult{kind: kind, hasResult: true, fillable: true, res: database.BookFileIntroResult{
			Status: status, Transcript: &text, At: now, Done: true,
			Title: fields.Title, Author: fields.Author, Narrator: fields.Narrator,
			Translator: fields.Translator, CoverArtist: fields.CoverArtist,
			Meta: &database.IntroMeta{Model: r.Model, Language: r.Language, ClipLengthSec: fileIntroClipSeconds},
		}}
	}
}

// donorResult copies a done file's stored result for a group with an identical
// hash. hasResult is false when the donor cannot be read or no longer holds a
// result for this hash; the caller then transcribes the group normally.
func (w *fileIntroWorker) donorResult(g fileIntroGroup) groupResult {
	d, err := w.store.GetBookFileByID(g.DonorBookID, g.DonorFileID)
	if err != nil || d == nil || d.IntroSourceHash != g.Hash || d.IntroTranscription == nil {
		return groupResult{}
	}
	// A fresh timestamp, never the donor's: a book fill is recognised (and
	// reverted by clear_run_id) by its time matching the file's, so a copy
	// carrying the donor's time could make this run's clear undo the DONOR
	// run's fill of its own book.
	at := time.Now()
	status := statusOK
	if d.TranscribeStatus != nil {
		status = *d.TranscribeStatus
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	text := *d.IntroTranscription
	gr := groupResult{kind: "reuse", reuse: true, hasResult: true, res: database.BookFileIntroResult{
		Status: status, Error: str(d.TranscribeError), Transcript: &text, At: at, Done: true, Meta: d.IntroMeta,
		Title: str(d.TranscribedTitle), Author: str(d.TranscribedAuthor), Narrator: str(d.TranscribedNarrator),
		Translator: str(d.TranscribedTranslator), CoverArtist: str(d.TranscribedCoverArtist),
	}}
	gr.fillable = text != transcribe.SilenceSentinel && (status == statusOK || status == statusUnparsed)
	return gr
}

// applyGroup writes one group's result to every member and counts it.
func (w *fileIntroWorker) applyGroup(g fileIntroGroup, gr groupResult) (int, error) {
	if !gr.hasResult {
		// Cannot happen for a group that got past runBatch; defensive.
		return 0, nil
	}
	log := w.reporter.Logger()
	settled := 0
	for _, m := range g.Members {
		if gr.kind == "deferred" {
			w.accum.update(func(s *database.FileIntroStats) { s.Deferred++ })
			settled++
			continue
		}
		r := gr.res
		r.SourceHash = g.Hash
		r.RunOpID = w.runID
		r.FillOnly = w.onlyMissing
		if r.At.IsZero() {
			r.At = time.Now()
		}
		wrote, err := w.intros.SetBookFileIntro(m.BookID, m.FileID, r)
		if err != nil {
			log.Warn("transcribe-file-intros: write failed", "file_id", m.FileID, "err", err)
			w.accum.update(func(s *database.FileIntroStats) { s.WriteErrors++ })
			settled++
			continue
		}
		settled++
		w.count(gr, wrote)
		if wrote == database.IntroWriteWrote && m.Ordinal == 0 && gr.fillable {
			if err := w.fillBook(m.BookID, r, gr); err != nil {
				log.Warn("transcribe-file-intros: book fill failed", "book_id", m.BookID, "err", err)
			}
		}
	}
	return settled, nil
}

func (w *fileIntroWorker) count(gr groupResult, wrote database.BookFileIntroWrite) {
	w.accum.update(func(s *database.FileIntroStats) {
		switch wrote {
		case database.IntroWriteStale:
			s.Stale++
			return
		case database.IntroWriteSkipped:
			s.AlreadyHad++
			return
		}
		if gr.reuse {
			s.Reused++
		} else {
			s.Attempted++
			switch gr.kind {
			case "ok":
				s.OK++
			case "unparsed":
				s.Unparsed++
			case "silence":
				s.Silence++
			case "whisper_error":
				s.WhisperError++
			case "source_missing":
				s.SourceMissing++
			case "ffmpeg_error":
				s.FFmpegError++
			}
		}
		if gr.res.Transcript != nil {
			s.TranscribedNow++
		}
	})
}

// fillBook writes a book-level transcript from its ordinal-0 file, and only
// when the book has none: ErrSkipBookWrite on any existing text (the silence
// sentinel counts as text, so a book already marked silent is left to the book
// op's longer-clip retries). It stamps the same At on the book as on the file:
// that equality is how clear_run_id later recognises its own fill.
func (w *fileIntroWorker) fillBook(bookID string, r database.BookFileIntroResult, gr groupResult) error {
	wrote := false
	_, err := w.store.ModifyBook(bookID, func(cur *database.Book) error {
		if cur.IntroTranscription != nil && *cur.IntroTranscription != "" {
			return database.ErrSkipBookWrite
		}
		at, status := r.At, r.Status
		text := *r.Transcript
		cur.IntroTranscription = &text
		cur.IntroTranscribedAt = &at
		cur.TranscribeAttemptedAt = &at
		cur.TranscribeStatus = &status
		cur.TranscribeError = nil
		set := func(dst **string, v string) {
			if v != "" {
				s := v
				*dst = &s
			}
		}
		set(&cur.TranscribedTitle, r.Title)
		set(&cur.TranscribedAuthor, r.Author)
		set(&cur.TranscribedNarrator, r.Narrator)
		set(&cur.TranscribedTranslator, r.Translator)
		set(&cur.TranscribedCoverArtist, r.CoverArtist)
		wrote = true
		return nil
	})
	if err != nil {
		return err
	}
	if wrote {
		w.accum.update(func(s *database.FileIntroStats) { s.BookFills++ })
	}
	return nil
}
