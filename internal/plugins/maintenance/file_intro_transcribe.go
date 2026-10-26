// file: internal/plugins/maintenance/file_intro_transcribe.go
// version: 1.5.0
// guid: 2a9d6f41-c803-4be7-95a0-7d14e8b3c6f2
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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

// fileIntroBusyOps are the operations the run will not write alongside: both
// rewrite book_file rows in bulk. The scheduler cannot do this for us (see the
// Writes note on the def), so the run refuses to start while one is active and
// re-checks immediately before every batch of writes.
//
// The set is every op that reaches the scanner's unlocked batch upsert:
// library.scan, library.import (its scanner.ScanRequest path) and
// library.folder-auto-scan (ProcessBooksParallel), plus the bulk metadata apply.
// itunes.import is NOT here: it creates rows with CreateBookFile and edits them
// with UpdateBookFile, which read under the row's lock, so it cannot erase an
// intro result between a read and a commit.
var fileIntroBusyOps = map[string]bool{
	"library.scan":                true,
	"library.import":              true,
	"library.folder-auto-scan":    true,
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
		Description:     "Transcribes the first 90 seconds of every present audio file, not just each book's first, and stores the result on the file's own record (keyed by the file's hash, so identical files are transcribed once and a rescan keeps the result). The server does no audio decoding: each file's path goes to the whisper worker, which reads and cuts the clip itself. A book's first file is handled first, and its result fills the book's transcript only when that is empty; an existing book transcript is never overwritten. Resumable: a file is done once its stored marker matches its hash, so a restart or a repeat run does only what is left. The run refuses to start while a library scan or a bulk metadata apply is running, and holds its writes until one has finished. dry_run defaults to true: a run that states no mode only counts the work and estimates the time. Param clear_run_id=<op id> removes what that run wrote (dry run first for the count); it returns files to untranscribed and cannot restore a transcript a forced re-run (only_missing=false) replaced.",
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
		// writer that reads under the lock cannot revert the intro group because
		// the shared book_file merge classes keep it (bookFileFieldClasses). It
		// still writes, so it declares the write capabilities, which is what makes
		// v3 treat it as a writer.
		// TWO things this does not cover. (1) The scanner's batch upsert reads the
		// stored row WITHOUT the per-file lock, so a scan can overwrite a result
		// committed between its read and its commit; that is why the op refuses to
		// start while a scan runs and re-checks before every write batch
		// (fileIntroWaitWhileBusy), a gate that narrows the window rather than
		// closing it. (2) A whole-row book writer can lose a book fill that
		// landed between its read and its write; the file result survives and a
		// forced run or clear_run_id puts that right.
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
	// StruckBy is the row's IntroTimeoutWorker when its timeout strikes were
	// counted against its current bytes: the machines that already struck the
	// file, which the job is routed away from (see fileIntroJob.AvoidWorkers).
	// Empty when the row carries no strike on these bytes.
	StruckBy string
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

// fileHasResult is database.IntroResultPresent, the one definition selection
// and the store's write rules share (see its doc for the legacy-result rule).
func fileHasResult(f *database.BookFileCore) bool { return f.HasIntroResult() }

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
			item := fileIntroItem{BookID: bookID, FileID: f.ID, Path: f.FilePath, Ordinal: ord}
			if f.IntroTimeouts > 0 && f.IntroTimeoutHash == f.FileHash {
				item.StruckBy = f.IntroTimeoutWorker
			}
			g.Members = append(g.Members, item)
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
		return p.clearFileIntros(ctx, store, p.deps.OperationQueueStore(), reporter, params.ClearRunID, dryRun)
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
	// A live run refuses to start while a scan or bulk apply is running (see
	// fileIntroWaitWhileBusy for why); the preview above never reaches here.
	if err := fileIntroRefuseIfBusy(queue); err != nil {
		return err
	}

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
		// One worker per whisper slot across all endpoints. Each call carries one
		// batch; the dispatcher places it on an endpoint with a free slot,
		// rotating among equal-priority endpoints (allocateJobsWith), so the
		// workers spread over every Mac instead of queueing on the first. The
		// transport still enforces each endpoint's own cap.
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
		"whisper_error", st.WhisperError, "ffmpeg_error", st.FFmpegError, "transcribe_timeout", st.TranscribeTimeout, "source_missing", st.SourceMissing, "reused", st.Reused,
		"deferred", st.Deferred, "stale", st.Stale, "already_had", st.AlreadyHad, "book_fills", st.BookFills,
		"coverage_now", st.TranscribedNow, "eligible", st.Eligible, "err", err)
	if err != nil {
		return err
	}
	_ = reporter.UpdateProgress(1, 1, mark+fmt.Sprintf(
		"Done — run %s: %d ok, %d unparsed, %d silent, %d whisper-err, %d ffmpeg-err, %d decode-timeout, %d source-missing, %d reused, %d deferred, %d stale; %d book transcripts filled; coverage %d/%d files",
		runID, st.OK, st.Unparsed, st.Silence, st.WhisperError, st.FFmpegError, st.TranscribeTimeout, st.SourceMissing, st.Reused, st.Deferred, st.Stale,
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
	// stuck counts consecutive batches the pool answered "worker stuck"; see
	// whisperStuckMaxBatches.
	stuck stuckStreak
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

// fileIntroBusyDef returns the DefID of the first queued or running operation
// the run must not write alongside, or "" when none is.
func fileIntroBusyDef(active []database.OperationV2Row) string {
	for _, op := range active {
		if fileIntroBusyOps[op.DefID] && (op.Status == "running" || op.Status == "queued") {
			return op.DefID
		}
	}
	return ""
}

// fileIntroRefuseIfBusy is the START gate: a live run (or a clear) refuses to
// begin while a scan or bulk apply is queued or running, and refuses when the
// queue cannot be read. A point-in-time check, like the dedupe op's; the
// per-batch write gate (fileIntroWaitWhileBusy before every write batch) is
// what covers a scan that starts later.
func fileIntroRefuseIfBusy(queue OpQueueReader) error {
	if queue == nil {
		return fmt.Errorf("%s: cannot verify no library scan is active; refusing to run", fileIntroOpID)
	}
	active, err := queue.ListActiveOperationsV2()
	if err != nil {
		return fmt.Errorf("%s: cannot list active operations; refusing to run: %w", fileIntroOpID, err)
	}
	if def := fileIntroBusyDef(active); def != "" {
		return fmt.Errorf("%s: %s is queued or running; refusing to start — a scan rewrites book_file rows with a "+
			"read-modify-write that can erase an intro result written between its read and its commit. Run this op "+
			"when the scan has finished. If that row is a stale zombie with no live handle, clear it rather than "+
			"bypassing this check", fileIntroOpID, def)
	}
	return nil
}

// waitWhileScanBusy blocks while a library scan or a bulk metadata apply
// is queued or running. It selects on ctx.Done in every wait, calls onWait (may
// be nil) with the blocking def each tick so the caller can re-announce itself
// to the watchdog, and fails (never proceeds) when the queue cannot be read
// repeatedly.
//
// WHY THIS GATES WRITES, not just batch starts: the scanner's batch upsert reads
// the stored book_file row WITHOUT the per-file lock SetBookFileIntro and
// ClearBookFileIntro take, then commits bare-row-plus-old-values. A result
// committed between that read and that commit is wiped (transcript, done marker
// and run id), and a cleared group can come back with its stale run id. The op
// therefore calls this immediately before every batch of writes. It narrows the
// race to the milliseconds between this return and the writes that follow it; it
// does not remove it, because only a lock the scanner also takes could.
//
// opID names the calling op in the errors. Both transcribe ops use it: the
// per-file op around every write batch, the per-book op around the book_file
// writes it makes (timeout strikes).
func waitWhileScanBusy(ctx context.Context, opID string, queue OpQueueReader, onWait func(def string)) error {
	if queue == nil {
		return fmt.Errorf("%s: cannot verify no library scan is active; refusing to run", opID)
	}
	listErrs := 0
	for {
		// A cancelled run writes nothing more: check before reading the queue,
		// not only while waiting, so a cancel that lands between two checks does
		// not let the caller go on to write.
		if err := ctx.Err(); err != nil {
			return err
		}
		active, err := queue.ListActiveOperationsV2()
		if err != nil {
			listErrs++
			if listErrs >= fileIntroPauseMaxListErrors {
				return fmt.Errorf("%s: cannot list active operations (%d tries): %w", opID, listErrs, err)
			}
		} else {
			listErrs = 0
			busy := fileIntroBusyDef(active)
			if busy == "" {
				return nil
			}
			if onWait != nil {
				onWait(busy)
			}
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

// scanBusyNow is the non-blocking form of the gate: nil when no scan or bulk
// apply is queued or running right now, an error naming it otherwise, and an
// error when the queue cannot be read (a write that cannot be shown safe is not
// made). The caller defers what it would have written.
func scanBusyNow(opID string, queue OpQueueReader) error {
	if queue == nil {
		return fmt.Errorf("%s: cannot verify no library scan is active", opID)
	}
	active, err := queue.ListActiveOperationsV2()
	if err != nil {
		return fmt.Errorf("%s: cannot list active operations: %w", opID, err)
	}
	if def := fileIntroBusyDef(active); def != "" {
		return fmt.Errorf("%s: %s is running", opID, def)
	}
	return nil
}

// fileIntroWaitWhileBusy is waitWhileScanBusy for the per-file op.
func fileIntroWaitWhileBusy(ctx context.Context, queue OpQueueReader, onWait func(def string)) error {
	return waitWhileScanBusy(ctx, fileIntroOpID, queue, onWait)
}

// waitWhileBusy is fileIntroWaitWhileBusy announcing the pause in the op's
// progress line.
func (w *fileIntroWorker) waitWhileBusy(ctx context.Context) error {
	return fileIntroWaitWhileBusy(ctx, w.queue, func(def string) {
		w.progress(introLiveMarker + "paused — " + def + " is running; resumes when it finishes")
	})
}

// groupResult is what a group's file result looks like before it is applied
// to each member.
type groupResult struct {
	res       database.BookFileIntroResult // SourceHash and RunOpID filled by the caller per member
	kind      string                       // "ok" "unparsed" "silence" "whisper_error" "source_missing" "deferred" "reuse"
	fillable  bool                         // a real transcript (not the silence sentinel, not a failure)
	reuse     bool
	hasResult bool
	// worker is the opaque identity of the machine that answered a
	// transcribe_timeout (transcribe.BatchResult.Worker), for its strike.
	worker string
}

// fileIntroOutageInterval is the base wait between retries of a batch whose
// endpoints were all unreachable (try n waits n x this). A var so tests do not
// wait minutes.
var fileIntroOutageInterval = time.Minute

// fileIntroOutageMaxTries is how many times one batch is attempted against an
// unreachable pool before the run stops. With the default interval that is
// 1+2+3+4 = 10 minutes of waiting, long enough for a worker restart or a
// brief network fault, short enough that a dead pool fails the run instead of
// silently skipping the whole library.
const fileIntroOutageMaxTries = 5

// errFileIntroOutage marks a batch that exhausted its retries against an
// unreachable pool.
var errFileIntroOutage = errors.New("whisper endpoints unreachable")

// callWhisper sends one batch, retrying the SAME batch with a growing,
// ctx-aware wait while every endpoint is unreachable (a recognized network
// failure says nothing about the files, so nothing is written meanwhile). It re-announces
// itself in the progress line each wait so the watchdog sees the op alive. Any
// other outcome, success or a per-batch error, is returned as is.
func (w *fileIntroWorker) callWhisper(ctx context.Context, jobs map[string]fileIntroJob) (map[string]fileIntroWhisperResult, error) {
	log := w.reporter.Logger()
	// settled accumulates what the pool answered across tries: a try that
	// failed part-way still answered some clips, and the next try sends only
	// the rest instead of paying the GPU for them twice.
	settled := make(map[string]fileIntroWhisperResult, len(jobs))
	// answered is settled, or nil when nothing was answered, for an error return.
	answered := func() map[string]fileIntroWhisperResult {
		if len(settled) == 0 {
			return nil
		}
		return settled
	}
	pending := jobs
	for try := 1; ; try++ {
		out, err := func() (map[string]fileIntroWhisperResult, error) {
			// A request can outlast the watchdog's progress budget (a chunk of
			// clips each allowed up to the worker's per-clip deadline), so say
			// the run is alive while it waits. Deferred, so the ticker stops even
			// if the call panics.
			defer tickWhileWaiting(whisperWaitTickInterval, func(elapsed time.Duration) {
				w.progress(introLiveMarker + fmt.Sprintf("waiting on whisper: %d files in flight for %s",
					len(pending), elapsed.Round(time.Second)))
			})()
			return fileIntroWhisperFn(ctx, pending, nil)
		}()
		if err != nil {
			// A try that failed produced its timeouts and crashes under conditions
			// that say nothing about the clips: they are not settled, so they are
			// re-sent (or, when the run gives up, deferred), never struck. What the
			// dispatcher itself condemned stands. See dropPoolStrikes.
			out = dropPoolStrikes(out, func(r fileIntroWhisperResult) (bool, bool) {
				return isTimeoutResult(r.Error), r.Condemned
			})
		}
		maps.Copy(settled, out)
		if err == nil {
			return settled, nil
		}
		// A worker that is reachable but wedged is not an outage: waiting and
		// re-sending the same batch parks it behind the stuck clip, which is how
		// one bad file took out the whole pool. The pool already split the batch
		// and retried the parts (transcribe.dispatchPool); what is left here is
		// a batch that could not be settled, so hand it back to runBatch.
		if transcribe.IsWorkerStuck(err) {
			return answered(), err
		}
		var te *transcribe.TransportError
		// Only a RECOGNIZED network failure (refused, reset, timeout, DNS), or a
		// pool the dispatcher already waited on and found still unavailable
		// (transcribe.ErrPoolUnavailable: every endpoint benched, or every
		// decoder failing to start), is an outage worth waiting out. An
		// unrecognized batch failure (a 5xx, a decode error, a capability
		// refusal, a worker fault) will not heal by itself, and re-sending it
		// would bench the endpoints process-wide and stall the book op that
		// shares them; it is returned as is and the batch defers. A worker fault
		// in particular must not be waited out: re-sending the same clips into
		// the same fault five times and then stopping the run stopped every
		// later run at the same batch.
		if !errors.As(err, &te) || !(te.Recognized || transcribe.IsPoolUnavailable(err)) {
			return answered(), err
		}
		if try >= fileIntroOutageMaxTries {
			return answered(), fmt.Errorf("%s: %w: %d tries in a row for one batch of %d files; stopping the run, nothing was written for the files still unanswered and they stay undone: %w",
				fileIntroOpID, errFileIntroOutage, try, len(jobs), err)
		}
		rest := make(map[string]fileIntroJob, len(pending))
		for k, j := range pending {
			if _, done := settled[k]; !done {
				rest[k] = j
			}
		}
		if len(rest) == 0 {
			return settled, nil
		}
		pending = rest
		wait := time.Duration(try) * fileIntroOutageInterval
		log.Warn("transcribe-file-intros: endpoints unreachable, retrying the batch (nothing written)",
			"jobs", len(pending), "try", try, "of", fileIntroOutageMaxTries, "retry_in", wait.String(), "endpoints", te.Endpoints, "err", te.Err.Error())
		w.progress(introLiveMarker + fmt.Sprintf("whisper endpoints unreachable (try %d of %d); retrying in %s",
			try, fileIntroOutageMaxTries, wait.Round(time.Second)))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
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
		// Every member has the same bytes, so a machine that struck any of them
		// struck this audio: route the job away from all of them.
		var struck []string
		for _, m := range g.Members {
			if m.StruckBy != "" {
				struck = append(struck, m.StruckBy)
			}
		}
		jobs[key] = fileIntroJob{Path: path, Seconds: fileIntroClipSeconds, AvoidWorkers: strings.Join(struck, database.IntroTimeoutWorkerSeparator)}
		jobGroup[key] = gi
	}

	// stop, when set, is returned after this batch's results are written: the
	// run must end (the pool is down or wedged), but what the workers answered
	// before that is still applied.
	var stop error
	if len(jobs) > 0 {
		out, err := w.callWhisper(ctx, jobs)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		// answered fills the group of every job the pool answered, errors or
		// not: a per-file result that settled stands even when the batch as a
		// whole failed.
		answered := func() {
			now := time.Now()
			for key, gi := range jobGroup {
				if r, ok := out[key]; ok {
					results[gi] = whisperGroupResult(r, now)
				}
			}
		}
		// rest gives every job the pool did NOT answer the same result.
		rest := func(gr groupResult) {
			for _, gi := range jobGroup {
				if !results[gi].hasResult {
					results[gi] = gr
				}
			}
		}
		deferred := groupResult{kind: "deferred", hasResult: true}
		switch {
		case errors.Is(err, errFileIntroOutage):
			// The endpoints stayed unreachable through every retry: stop the run
			// rather than drain every remaining batch as deferred. Whatever was
			// answered before that is still written.
			answered()
			rest(deferred)
			stop = err
		case transcribe.IsWorkerStuck(err):
			// Reachable but wedged, and the pool could not narrow it to a file:
			// the unanswered files stay undone. Several in a row means the pool
			// itself is wedged, which a run cannot wait out. A batch that
			// answered something did get an answer, so it does not extend the
			// streak.
			n := int64(0)
			if len(out) == 0 {
				n = w.stuck.hit()
			} else {
				w.stuck.reset()
			}
			log.Warn("transcribe-file-intros: whisper worker stuck, unanswered files deferred (nothing written for them)",
				"jobs", len(jobs), "answered", len(out), "stuck_batches_in_a_row", n, "of", whisperStuckMaxBatches, "err", err.Error())
			answered()
			rest(deferred)
			if n >= whisperStuckMaxBatches {
				stop = fmt.Errorf("%s: %w: %d batches in a row; stopping the run, nothing was written for them and the files stay undone (restart the stuck workers): %w",
					fileIntroOpID, errWhisperStuck, n, err)
			}
		case transcribe.IsTransport(err):
			// A batch-level failure that is not a recognized outage (so it was
			// not retried): it says nothing about any file, so nothing is
			// written for the unanswered ones and they stay undone.
			log.Warn("transcribe-file-intros: whisper batch failed at batch level, unanswered files deferred (nothing written for them)",
				"jobs", len(jobs), "answered", len(out), "err", err.Error())
			answered()
			rest(deferred)
		case err != nil:
			log.Warn("transcribe-file-intros: whisper batch failed", "jobs", len(jobs), "answered", len(out), "err", err)
			detail := truncateDetail(err.Error())
			answered()
			rest(groupResult{kind: "whisper_error", hasResult: true, res: database.BookFileIntroResult{
				Status: statusWhisperError, Error: detail,
			}})
		default:
			w.stuck.reset() // the pool answered
			for key, gi := range jobGroup {
				if _, ok := out[key]; !ok {
					// No answer is not silence. An absent key is a transport
					// that dropped the job, so the file stays undone.
					log.Warn("transcribe-file-intros: no result for a sent job, deferred", "file_id", key)
					results[gi] = deferred
				}
			}
			answered()
		}
	}

	// WRITE GATE. The whisper call above can take minutes, so the check at the
	// top of this function is stale by now: a scan may have started meanwhile.
	// Hold the results (a few files' worth of text) until none is running, then
	// write at once. Nothing is lost if the run is cancelled here: the files are
	// still undone and the next run picks them up.
	if err := w.waitWhileBusy(ctx); err != nil {
		return settled, err
	}
	for gi, g := range groups {
		gr := results[gi]
		n, err := w.applyGroup(g, gr)
		settled += n
		if err != nil {
			return settled, err
		}
	}
	return settled, stop
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
		//   transcribe_timeout, decoder_crashed, decoder_killed -> the decoder ran
		//                   past its deadline on it, died holding it, or was
		//                   killed from outside holding it: one strike is
		//                   retried, transcribeTimeoutStrikeLimit (each after the
		//                   first from a different worker) make it permanent like
		//                   ffmpeg_error;
		//   path_unmapped, ffmpeg_timeout, container_rejected, and every
		//                   no-answer code (transcribe.IsNoAnswer:
		//                   worker_misconfigured, request_abandoned,
		//                   worker_fault) -> the mount, the
		//                   share or the worker's own setup or policy, not the
		//                   file: written as nothing, the file stays undone;
		//   anything else (whisper_error, bad_request, no code) -> whisper_error,
		//                   retried by the next run.
		detail := truncateDetail(r.Error)
		if transcribe.IsNoAnswer(r.Error) {
			// No answer about the file (a decoder that cannot start, an abandoned
			// request, a job the dispatcher deferred because only faulted workers
			// answered it): written as nothing, the file stays undone.
			return groupResult{kind: "deferred", hasResult: true}
		}
		switch transcribe.PathErrorCode(r.Error) {
		case transcribe.PathErrNotFound:
			return groupResult{kind: "source_missing", hasResult: true, res: database.BookFileIntroResult{
				Status: statusSourceMissing, Error: detail, At: now}}
		case transcribe.PathErrFFmpeg:
			return groupResult{kind: "ffmpeg_error", hasResult: true, res: database.BookFileIntroResult{
				Status: statusFFmpegError, Error: detail, At: now}}
		case transcribe.PathErrTranscribeTimeout, transcribe.PathErrDecoderCrashed, transcribe.PathErrDecoderKilled:
			// The worker's decoder ran past its per-clip deadline on this file
			// and was killed and restarted, died while it held the clip, or was
			// killed from outside while it held it. A verdict about the audio,
			// but only after transcribeTimeoutStrikeLimit of them, each after
			// the first from a machine that has not struck the file: applyGroup
			// counts the strike (below the limit the file is deferred, at it the
			// status becomes permanent).
			return groupResult{kind: "transcribe_timeout", hasResult: true, worker: r.Worker, res: database.BookFileIntroResult{
				Status: statusTranscribeTimeout, Error: detail, At: now}}
		case transcribe.PathErrUnmapped, transcribe.PathErrFFmpegTimeout, transcribe.PathErrContainerRejected:
			// The mount, the share or the worker's own setup or policy, not a
			// verdict on the file: written as nothing, the file stays undone.
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
			Status: statusEmpty, Error: "whisper returned empty text", Transcript: &s, At: now, Done: true, Silence: true,
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
		Silence: text == transcribe.SilenceSentinel,
		Title:   str(d.TranscribedTitle), Author: str(d.TranscribedAuthor), Narrator: str(d.TranscribedNarrator),
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
		if gr.kind == "transcribe_timeout" {
			w.recordTimeout(g, m, gr)
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
		if (wrote == database.IntroWriteWrote || wrote == database.IntroWriteReplaced) && m.Ordinal == 0 && gr.fillable {
			if err := w.fillBook(m.BookID, r, gr); err != nil {
				log.Warn("transcribe-file-intros: book fill failed", "book_id", m.BookID, "err", err)
			}
		}
	}
	return settled, nil
}

// recordTimeout counts one transcribe_timeout strike against a member file.
// Below the limit the file is deferred (retried next run, nothing about it
// written but the strike); at the limit it carries the permanent
// transcribe_timeout status, which selection skips.
func (w *fileIntroWorker) recordTimeout(g fileIntroGroup, m fileIntroItem, gr groupResult) {
	log := w.reporter.Logger()
	out, err := w.intros.RecordIntroTimeout(m.BookID, m.FileID, database.IntroTimeoutStrike{
		SourceHash: g.Hash, RunOpID: w.runID, Detail: gr.res.Error, At: gr.res.At,
		Limit: transcribeTimeoutStrikeLimit, MinGap: transcribeStrikeMinGap, Worker: gr.worker,
	})
	switch {
	case err != nil:
		log.Warn("transcribe-file-intros: recording a transcribe_timeout strike failed", "file_id", m.FileID, "err", err.Error())
		w.accum.update(func(s *database.FileIntroStats) { s.WriteErrors++ })
	case out.SameRun:
		// This run already counted a strike for these bytes (a file that is in
		// two groups of one run). One strike per file per run: deferred.
		log.Info("transcribe-file-intros: transcribe_timeout already counted for this file in this run, deferred without a second strike",
			"file_id", m.FileID, "strikes", out.Strikes)
		w.accum.update(func(s *database.FileIntroStats) { s.Deferred++ })
	case out.TooSoon:
		// The file's previous strike (from either op) is less than the minimum
		// gap old: the same stall seen again, not a second occasion.
		log.Info("transcribe-file-intros: transcribe_timeout too soon after the file's last strike, deferred without a second strike",
			"file_id", m.FileID, "strikes", out.Strikes, "min_gap", transcribeStrikeMinGap.String())
		w.accum.update(func(s *database.FileIntroStats) { s.Deferred++ })
	case out.SameWorker:
		// The file's previous strike came from this same worker: one sick worker
		// on two nights is not two pieces of evidence about the audio.
		log.Info("transcribe-file-intros: transcribe_timeout from the same worker as the file's last strike, deferred without a second strike",
			"file_id", m.FileID, "strikes", out.Strikes, "worker", gr.worker)
		w.accum.update(func(s *database.FileIntroStats) { s.Deferred++ })
	case out.Stale:
		w.accum.update(func(s *database.FileIntroStats) { s.Stale++ })
	case out.Skipped:
		w.accum.update(func(s *database.FileIntroStats) { s.AlreadyHad++ })
	case out.Permanent:
		log.Warn("transcribe-file-intros: transcribe_timeout is now permanent for this file",
			"file_id", m.FileID, "strikes", out.Strikes, "limit", transcribeTimeoutStrikeLimit)
		w.accum.update(func(s *database.FileIntroStats) {
			s.Attempted++
			s.TranscribeTimeout++
		})
	default:
		log.Warn("transcribe-file-intros: transcribe_timeout strike recorded, file deferred to the next run",
			"file_id", m.FileID, "strikes", out.Strikes, "limit", transcribeTimeoutStrikeLimit)
		w.accum.update(func(s *database.FileIntroStats) { s.Deferred++ })
	}
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
		// Coverage grows only when the row held no result before this write; a
		// forced re-run replacing a transcript (IntroWriteReplaced) does not.
		if gr.res.Transcript != nil && wrote == database.IntroWriteWrote {
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
