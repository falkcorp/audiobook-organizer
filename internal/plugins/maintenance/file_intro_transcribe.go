// file: internal/plugins/maintenance/file_intro_transcribe.go
// version: 1.10.0
// guid: 2a9d6f41-c803-4be7-95a0-7d14e8b3c6f2
// last-edited: 2026-11-17

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

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
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
// checkpoint hook is not called by RunItems under concurrency anyway). The
// native kind adds one: near its deadline it checkpoints a cursor (see
// fileIntroCursor) and yields to a fresh attempt, which HEAD's 30-day timeout
// never needed.

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
	// writeMu orders the persisted snapshots: flush holds it across the copy
	// AND the Put, so the snapshot written last is the one copied last. Lock
	// order is always writeMu then mu; mu stays short-held (counter updates
	// never wait on a sink write), and nothing under mu may call flush.
	writeMu sync.Mutex
	mu      sync.Mutex
	stats   database.FileIntroStats
	sink    t6g4FileIntroStatsSink
	log     *slog.Logger // reporter's logger; nil in tests that do not care
}

// t6g4FileIntroStatsSink persists the run's counters: the one method of
// database.FileIntroStatsStore the accumulator calls.
type t6g4FileIntroStatsSink interface {
	PutFileIntroStats(*database.FileIntroStats) error
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
//
// The copy and the Put are one step under writeMu. Copying under mu alone and
// writing after releasing it let two workers flushing at once land the older
// snapshot last, so the persisted stats (and the status page reading them) went
// backwards mid-run.
func (a *fileIntroAccum) flush(done bool) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.mu.Lock()
	a.stats.UpdatedAt = time.Now()
	a.stats.Done = done
	cp := a.stats
	a.mu.Unlock()
	// The counters in the result (Done included) do not depend on a sink: a run
	// started with no stats sink still reports that it finished.
	if a.sink == nil {
		return
	}
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

// fileIntroKind is the native kind of maintenance.transcribe-file-intros (rule R3).
const fileIntroKind tasks.Kind = "books." + fileIntroOpID

// fileIntroEnv is what the core needs from its engine (rules R2 and R10).
type fileIntroEnv struct {
	t6g4Base
	// YieldMargin is how close to the context's deadline the run stops starting
	// batches and yields (tasks.ErrContinueInPlace). 0 (HEAD's shell) never
	// yields; the native shell sets fileIntroYieldMargin. It has an effect only
	// with a SaveCheckpoint, which a yield cannot do without.
	YieldMargin time.Duration
	// CallMargin is how long before the context's deadline a whisper call is cut
	// (fileIntroCallMargin): the call, the dispatcher's pacing included, runs
	// under a context that ends then, once this attempt's batch 0 has completed,
	// and a call cut that way yields instead of ending the run timed_out. 0 (HEAD)
	// never cuts a call. Like YieldMargin it has an effect only with a
	// SaveCheckpoint.
	CallMargin time.Duration
}

// fileIntroNativeParams is fileIntroParams for the native run: the mode comes
// from the start request, so dry_run, dryRun and apply are accepted only to be
// refused.
type fileIntroNativeParams struct {
	OnlyMissing *bool  `json:"only_missing,omitempty"`
	RetryFailed *bool  `json:"retry_failed,omitempty"`
	ClearRunID  string `json:"clear_run_id,omitempty"`
	t6g4ModeKeys
}

// fileIntroResult is the run's outcome: the selection (always), the counters of
// a live run, or the rollback's tallies.
type fileIntroResult struct {
	DryRun   bool                     `json:"dry_run"`
	RunID    string                   `json:"run_id,omitempty"`
	Eligible int                      `json:"eligible"`
	Selected int                      `json:"selected"`
	Stats    *database.FileIntroStats `json:"stats,omitempty"`
	Cleared  *fileIntroClearResult    `json:"cleared,omitempty"`
}

// fileIntroWorkerReads is the one row read the worker makes: a donor file.
type fileIntroWorkerReads interface {
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
}

// fileIntroWorkerWrites is what the worker writes: a file's result, a timeout
// strike, and a book-level fill.
type fileIntroWorkerWrites interface {
	SetBookFileIntro(bookID, fileID string, r database.BookFileIntroResult) (database.BookFileIntroWrite, error)
	RecordIntroTimeout(bookID, fileID string, s database.IntroTimeoutStrike) (database.IntroTimeoutOutcome, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// runFileIntroTranscribe is the v2 shell around runFileIntroTranscribeCore (rule R2 item 2).
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
	_, err = p.runFileIntroTranscribeCore(ctx, params, fileIntroEnv{
		t6g4Base: t6g4HeadBase(reporter, !dryRun, headT6G4Store{OpsStore: store, deps: p.deps}, headT6G4Writer{store: store}).withStats(headT6G4Stats{store: store}),
	})
	return err
}

// fileIntroTranscribeKind is the native shell (rule R2 item 3). HEAD has a
// preview, so a preview run counts and writes nothing.
func (p *Plugin) fileIntroTranscribeKind(deps t6g4Deps, sched *tasks.Schedule) tasks.Definition {
	return tasks.Task(fileIntroKind, tasks.TaskSpec[fileIntroNativeParams, fileIntroResult]{
		Common: tasks.Common{
			Title: "Transcribe file intros",
			Help:  "Transcribes the first 90 seconds of every present audio file, not just each book's first, and stores the result on the file's own record (keyed by the file's hash, so identical files are transcribed once and a rescan keeps the result). The server does no audio decoding: each file's path goes to the whisper worker, which reads and cuts the clip itself. A book's first file is handled first, and its result fills the book's transcript only when that is empty; an existing book transcript is never overwritten. Resumable: a file is done once its stored marker matches its hash, so a restart or a repeat run does only what is left. The run refuses to start while a library scan or a bulk metadata apply is running, and holds its writes until one has finished. A preview only counts the work and estimates the time. Param clear_run_id=<op id> removes what that run wrote (preview first for the count); it returns files to untranscribed and cannot restore a transcript a forced re-run (only_missing=false) replaced.",
			// HEAD declares no Writes (see the long comment on fileIntroTranscribeDef).
			// The body writes a file's intro group (SetBookFileIntro, ClearBookFileIntro,
			// RecordIntroTimeout: book_files), a book's transcript fill (ModifyBook:
			// books). ModifyBook also stamps the metadata cache stale on an identity
			// change and stages the ident: rows (metadata_cache, indexes: the shared
			// ModifyBook row, kinds_resources.go). The run's counters (the
			// stats:transcribe-files key) have no pkg/ops constant and are written
			// outside the gated writer on purpose: see t6g4StatsSink.
			Effects:  tasks.Writes(t6g4Res(ops.ResBookFiles, ops.ResBooks, ops.ResMetadataCache, ops.ResIndexes)...),
			Perms:    tasks.Permissions{Start: t6g4StartRequirement()},
			Resume:   tasks.ResumeContinue,
			Priority: tasks.PriorityLow,
			// HEAD's 30 days is above tasks.MaxTimeout. A timeout is terminal, so the
			// run does not wait for it: it stops starting batches and yields
			// (tasks.ErrContinueInPlace) fileIntroYieldMargin before the deadline,
			// and the next attempt continues from the checkpoint. See
			// fileIntroBatchLoop.run for what can still reach the timeout.
			ConcurrencyKey:  tasks.Key("books." + fileIntroOpID),
			Timeout:         tasks.MaxTimeout,
			ProgressTimeout: 30 * time.Minute,
			Schedule:        sched,
			Ready:           []tasks.ReadySignal{t6g4SignalMemDB},
		},
		Run: func(rc *tasks.RC, in fileIntroNativeParams) (fileIntroResult, error) {
			if err := t6g4RefuseModeKeys(rc.Kind(), in.t6g4ModeKeys); err != nil {
				return fileIntroResult{}, err
			}
			base, err := t6g4NativeBase(rc, deps, true)
			if err != nil {
				return fileIntroResult{}, err
			}
			// Only the native shell sets the margins: HEAD's builds the env without
			// either, so only a native run yields before its deadline (review M4)
			// and cuts a whisper call before it (N16). A test drives this shell to a
			// blocked call to pin that it hands both over (N32).
			return p.runFileIntroTranscribeCore(rc.Context(), fileIntroParams{
				OnlyMissing: in.OnlyMissing, RetryFailed: in.RetryFailed, ClearRunID: in.ClearRunID,
			}, fileIntroEnv{t6g4Base: base, YieldMargin: fileIntroYieldMargin, CallMargin: fileIntroCallMargin()})
		},
	})
}

// runFileIntroTranscribeCore is HEAD's body (rule R2 item 1) after the shell
// resolved the store, decoded params and resolved the mode: env.Live is true
// exactly when HEAD's dryRun is false. It reads through env.Store and writes
// only through env.Writer, except the stats:transcribe-files counters
// (env.Stats).
func (p *Plugin) runFileIntroTranscribeCore(ctx context.Context, params fileIntroParams, env fileIntroEnv) (fileIntroResult, error) {
	store := env.Store
	dryRun := !env.Live
	reporter := env.reporter(ctx)
	var queue OpQueueReader
	if store.HasOperationQueue() {
		queue = store
	}
	if params.ClearRunID != "" {
		cleared, err := p.t6g4ClearFileIntros(ctx, fileIntroClearIO{
			reads: store, writes: env.Writer, writable: store.FileIntroWritesAvailable(), each: env.Each,
		}, queue, reporter, params.ClearRunID, dryRun)
		return fileIntroResult{DryRun: dryRun, RunID: params.ClearRunID, Cleared: &cleared}, err
	}

	onlyMissing := params.OnlyMissing == nil || *params.OnlyMissing
	retryFailed := params.RetryFailed != nil && *params.RetryFailed
	mark := introModeMarker(dryRun)
	log := env.Logger()

	// An attempt that yielded (or was cut off) left a cursor: the run's id and
	// counters, how many batches it completed, and the last group of the
	// contiguous completed prefix. HEAD never checkpoints this op (its base has
	// no LoadCheckpoint), so there it is always a fresh run. It is read before
	// the first progress line so a continued attempt's bar starts at the batches
	// already done instead of dropping back to zero while it selects.
	var prior fileIntroCursor
	resumed := false
	if !dryRun && env.LoadCheckpoint != nil {
		ok, lerr := env.LoadCheckpoint(&prior)
		if lerr != nil {
			return fileIntroResult{}, fmt.Errorf("%s: restore checkpoint: %w", fileIntroOpID, lerr)
		}
		resumed = ok
	}
	base := 0
	if resumed {
		base = prior.DoneBatches
	}

	env.Progress(base, base+1, mark+"Selecting work — reading every book file")
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return fileIntroResult{}, fmt.Errorf("%s: read book files: %w", fileIntroOpID, err)
	}
	sel := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: onlyMissing, retryFailed: retryFailed})
	cores = nil
	res := fileIntroResult{DryRun: dryRun, Eligible: sel.eligible, Selected: sel.selected}

	if dryRun {
		return res, previewFileIntros(reporter, sel, onlyMissing)
	}

	slots := fileIntroSlots()
	if slots < 1 {
		return res, fmt.Errorf("%s: no whisper endpoint is configured (whisper_endpoints / whisper_remote_url); refusing to start a run that cannot transcribe", fileIntroOpID)
	}
	if err := fileIntroPreflight(ctx); err != nil {
		return res, err
	}
	if !store.FileIntroWritesAvailable() {
		return res, fmt.Errorf("%s: the store does not support per-file intro writes", fileIntroOpID)
	}
	// A live run refuses to start while a scan or bulk apply is running (see
	// fileIntroWaitWhileBusy for why); the preview above never reaches here.
	//
	// A RESUMED attempt waits for the scan instead (review N17). It is
	// re-queued at once after a yield, and a yield is often caused by a scan:
	// the scan-busy give-up leaves a batch undone because a scan was running at
	// the margin, so the next attempt very likely starts while it still is.
	// Refusing then would end the run failed and lose the yield (HEAD, which
	// never resumes, keeps the refusal for a fresh start). The wait is bounded by
	// ctx only: it cannot yield again, because a yield needs this attempt to have
	// completed a batch (the executor's progress proof), so a scan that outlasts
	// a whole attempt ends the run timed_out, as a batch 0 parked in a wait does.
	if resumed {
		if err := waitWhileScanBusy(ctx, fileIntroOpID, queue, func(def string) {
			env.Progress(base, base+1, mark+"paused — "+def+" is running; resumes when it finishes")
		}); err != nil {
			return res, err
		}
	} else if err := fileIntroRefuseIfBusy(queue); err != nil {
		return res, err
	}

	runID := env.OpID
	if runID == "" {
		runID = "run-" + time.Now().UTC().Format(time.RFC3339)
	}

	if resumed && prior.RunID != "" {
		runID = prior.RunID
	}
	res.RunID = runID

	groups := sel.groups
	if resumed {
		// Only a forced run (only_missing=false) needs the prefix skipped: the
		// default run's selection already leaves out every file that has a result.
		if !onlyMissing && prior.Last != nil {
			groups = fileIntroSkipThrough(groups, *prior.Last)
		}
	}

	startedAt := time.Now()
	stats := database.FileIntroStats{
		RunOpID: runID, StartedAt: startedAt, UpdatedAt: startedAt,
		Eligible: sel.eligible, TranscribedAtStart: sel.transcribedAtStart, TranscribedNow: sel.transcribedAtStart,
		Selected:      sel.selected,
		SkippedNoHash: sel.skippedNoHash, SkippedMissing: sel.skippedMissing,
		SkippedUnsupported: sel.skippedUnsupported, SkippedFailed: sel.skippedFailed,
	}
	if resumed {
		// The counters describe the whole run, not this attempt.
		stats = prior.Stats
		stats.RunOpID = runID
		stats.UpdatedAt = startedAt
	}
	accum := &fileIntroAccum{sink: env.Stats, log: log, stats: stats}
	startAttrs := []any{
		"run_id", runID, "only_missing", onlyMissing, "retry_failed", retryFailed,
		"eligible", sel.eligible, "already_done", sel.transcribedAtStart,
		"selected", sel.selected, "groups", len(groups), "reuse_files", sel.reuseFiles, "slots", slots,
	}
	if resumed {
		// HEAD never resumes (no checkpoint), so its line keeps HEAD's attributes.
		startAttrs = append(startAttrs, "resumed", true, "batches_done_earlier", base)
	}
	log.Info("transcribe-file-intros: starting", startAttrs...)

	if len(sel.groups) == 0 && !resumed {
		accum.flush(true)
		env.Progress(1, 1, mark+fmt.Sprintf(
			"Done — nothing to transcribe (%d of %d eligible files already have a result)",
			sel.transcribedAtStart, sel.eligible))
		st := accum.snapshot()
		res.Stats = &st
		return res, nil
	}
	accum.flush(false)

	w := &fileIntroWorker{
		store: store, writer: env.Writer, queue: queue, reporter: reporter, accum: accum,
		runID: runID, onlyMissing: onlyMissing,
	}
	batches := chunkFileIntroGroups(groups, fileIntroBatchGroups)
	w.batchesTotal = base + len(batches)
	w.batchesDone.Store(int64(base))
	remainingFiles := 0
	for _, g := range groups {
		remainingFiles += len(g.Members)
	}
	var filesDone atomic.Int64
	loop := &fileIntroBatchLoop{
		batches: batches, base: base, slots: slots,
		runBatch: w.runBatch,
		afterBatch: func(n int) {
			w.batchesDone.Add(1)
			filesDone.Add(int64(n))
			accum.flush(false)
		},
		// The runner's own total is this attempt's batch count; the bar's is absolute.
		label: func(i, _ int) string {
			return mark + fmt.Sprintf("Batch %d/%d — %d of %d files handled", base+i+1, base+len(batches), int(filesDone.Load()), remainingFiles)
		},
		each: env.Each,
	}
	// saveCursor checkpoints the run after prefix more batches than the earlier
	// attempts completed.
	saveCursor := func(prefix int) error {
		cur := fileIntroCursor{RunID: runID, DoneBatches: base + prefix, Last: prior.Last, Stats: accum.snapshot()}
		if prefix > 0 {
			last := batches[prefix-1]
			k := fileIntroKeyOf(last[len(last)-1])
			cur.Last = &k
		}
		return env.SaveCheckpoint(cur)
	}
	if env.SaveCheckpoint != nil {
		loop.yieldMargin = env.YieldMargin
		if loop.yieldMargin > 0 {
			// The waits of the native run are bounded by the same margin as the
			// loop, and its whisper calls end CallMargin before the deadline (HEAD
			// leaves both nil and waits as before).
			w.giveUp = loop.shouldYield
			w.callMargin, w.batchZeroDone = env.CallMargin, loop.firstDone.Load
			// The cursor is saved every time the contiguous completed prefix
			// advances, not only at the yield (review N22): a crash or the nightly
			// deploy then resumes near where the run stopped, and the stats key does
			// not go backwards. The rate is at most one small JSON write per
			// completed batch (fileIntroBatchGroups files of whisper work), so
			// seconds apart per worker and never faster than the batches finish. A
			// failed write is logged and retried by the next advance or the yield.
			loop.onAdvance = func(prefix int) {
				if cerr := saveCursor(prefix); cerr != nil {
					log.Warn("transcribe-file-intros: cannot checkpoint after a batch; the next batch or the yield tries again",
						"run_id", runID, "batches_complete", base+prefix, "err", cerr)
				}
			}
		}
	}
	prefix, yielded, err := loop.run(ctx)
	if err == nil && yielded {
		if cerr := saveCursor(prefix); cerr != nil {
			accum.flush(false)
			return res, fmt.Errorf("%s: save checkpoint before yielding: %w", fileIntroOpID, cerr)
		}
		accum.flush(false)
		st := accum.snapshot()
		res.Stats = &st
		// Reset the bar to the exact prefix: RunItems counted the batches it
		// skipped at the margin as completed, and the next attempt's progress
		// proof is measured against this value.
		env.Progress(base+prefix, base+len(batches), mark+fmt.Sprintf(
			"Yielding before the deadline — %d of %d batches complete; continuing in place", base+prefix, base+len(batches)))
		log.Info("transcribe-file-intros: yielding before the deadline",
			"run_id", runID, "batches_complete", base+prefix, "batches_total", base+len(batches))
		return res, tasks.ErrContinueInPlace
	}
	accum.flush(true)
	st := accum.snapshot()
	res.Stats = &st
	log.Info("transcribe-file-intros: finished",
		"run_id", runID, "attempted", st.Attempted, "ok", st.OK, "unparsed", st.Unparsed, "silence", st.Silence,
		"whisper_error", st.WhisperError, "ffmpeg_error", st.FFmpegError, "transcribe_timeout", st.TranscribeTimeout, "source_missing", st.SourceMissing, "reused", st.Reused,
		"deferred", st.Deferred, "stale", st.Stale, "already_had", st.AlreadyHad, "book_fills", st.BookFills,
		"coverage_now", st.TranscribedNow, "eligible", st.Eligible, "err", err)
	if err != nil {
		return res, err
	}
	env.Progress(1, 1, mark+fmt.Sprintf(
		"Done — run %s: %d ok, %d unparsed, %d silent, %d whisper-err, %d ffmpeg-err, %d decode-timeout, %d source-missing, %d reused, %d deferred, %d stale; %d book transcripts filled; coverage %d/%d files",
		runID, st.OK, st.Unparsed, st.Silence, st.WhisperError, st.FFmpegError, st.TranscribeTimeout, st.SourceMissing, st.Reused, st.Deferred, st.Stale,
		st.BookFills, st.TranscribedNow, st.Eligible))
	return res, nil
}

// fileIntroCheckpointBudget is what the yield path is allowed after a whisper
// call has been cut: the checkpoint write, the final stats flush and the
// progress reset, with room for a slow store.
const fileIntroCheckpointBudget = 2 * time.Minute

// fileIntroCallMargin is how long before the attempt's deadline a whisper call
// is cut (fileIntroWorker.boundCall). After a cut the run still has to leave
// the call (its cancellation tail: the liveness verification after a response
// is not cancellable, about ten seconds of probes) and run the yield path (the
// checkpoint write, the final stats flush and the progress reset, the
// fileIntroCheckpointBudget). The pool's own pacing allowance,
// transcribe.PoolPaceMax (the dispatcher paces a refused request for that long
// before it gives up), is the third term: a request must not start where its
// own pacing could outlast what is left, and callBoundHit refuses to start one
// inside the margin. The margin is strictly larger than the allowance plus the
// budget, with a minute of slack; TestFileIntroMargins_* pins the inequality.
//
// It is a func variable for two reasons: PoolPaceMax is read from the pool, so a
// change there moves the margin with it, and the native-shell test replaces it
// (with fileIntroYieldMargin) by margins that fit a seconds-long deadline, so a
// shell that forgets to hand it to the run fails a test (N32). Nothing else
// assigns it.
var fileIntroCallMargin = func() time.Duration {
	return transcribe.PoolPaceMax() + fileIntroCheckpointBudget + time.Minute
}

// fileIntroYieldMargin is how close to its deadline a native attempt gets before
// it stops starting batches, stops waiting (scan-busy pause, outage retry) and
// yields (tasks.ErrContinueInPlace) to a fresh attempt. It is a minute beyond
// the call margin, so a batch is never started in the span where its whisper
// call would be cut at once. It is computed once, at init, from the call margin
// of that moment; the two could only diverge if the pool's pace allowance
// changed at run time, which only a test does. See fileIntroBatchLoop.run for
// what the margin does and does not bound.
var fileIntroYieldMargin = fileIntroCallMargin() + time.Minute

// fileIntroKey orders groups exactly as selectFileIntroWork does: the tier (0
// when any member is a book's first file), then the first member's book and
// ordinal. The sort is stable and keyed on the first member only, so it is a
// total order over groups that differ in those three fields; two groups that
// share all three would be a duplicate hash for the same first file, which
// selection cannot produce.
type fileIntroKey struct {
	Tier    int    `json:"tier"`
	BookID  string `json:"book_id"`
	Ordinal int    `json:"ordinal"`
}

func fileIntroKeyOf(g fileIntroGroup) fileIntroKey {
	m := g.Members[0]
	return fileIntroKey{Tier: g.tier(), BookID: m.BookID, Ordinal: m.Ordinal}
}

// atOrBefore reports k <= o in selection order.
func (k fileIntroKey) atOrBefore(o fileIntroKey) bool {
	if k.Tier != o.Tier {
		return k.Tier < o.Tier
	}
	if k.BookID != o.BookID {
		return k.BookID < o.BookID
	}
	return k.Ordinal <= o.Ordinal
}

// fileIntroSkipThrough returns the groups after the last one at or before last.
// groups is in selection order, so the skipped groups are a prefix.
func fileIntroSkipThrough(groups []fileIntroGroup, last fileIntroKey) []fileIntroGroup {
	n := 0
	for n < len(groups) && fileIntroKeyOf(groups[n]).atOrBefore(last) {
		n++
	}
	return groups[n:]
}

// fileIntroCursor is the native run's checkpoint, written when an attempt
// yields. Stats are the run's counters so far (the next attempt keeps counting
// from them); DoneBatches is the absolute number of completed batches, which
// keeps the progress bar monotone across attempts; Last is the last group of the
// contiguous completed prefix, nil when none has completed.
//
// Hazard of a forced run (only_missing=false): such a run has no done-marker to
// find its place by (every file is selected again, done or not), so the next
// attempt skips by KEY: every group at or before Last in selection order is taken
// to be done (fileIntroSkipThrough). That holds while the library stands still.
// A group that appears between the attempts AT OR BEFORE Last (a book imported,
// or a file whose hash changed so it moved tier or ordinal) is skipped, not
// transcribed, by that run; it is picked up by the next run, which starts again
// from the beginning. The default run is not exposed: its selection leaves out
// whatever has a result, so it needs no skip.
type fileIntroCursor struct {
	RunID       string                  `json:"run_id"`
	DoneBatches int                     `json:"done_batches"`
	Last        *fileIntroKey           `json:"last,omitempty"`
	Stats       database.FileIntroStats `json:"stats"`
}

// fileIntroBatchLoop runs the op's batches through the env's loop runner and,
// when yieldMargin is set, stops starting batches once the context's deadline is
// that close.
type fileIntroBatchLoop struct {
	batches [][]fileIntroGroup
	// base is how many batches earlier attempts completed: progress is reported
	// as base+i, against base+len(batches).
	base  int
	slots int
	// yieldMargin 0 never yields (HEAD).
	yieldMargin time.Duration
	runBatch    func(ctx context.Context, groups []fileIntroGroup) (int, error)
	afterBatch  func(settled int)
	// onAdvance, when set, is called with the new length of the contiguous
	// completed prefix each time it grows. Calls are serialised.
	onAdvance func(prefix int)
	label     func(i, total int) string
	each      t6g4EachFn

	// firstDone is set once this attempt's batch 0 has completed; yielding waits
	// for it, so a yield always leaves a prefix of at least one batch.
	firstDone atomic.Bool
	yielding  atomic.Bool
	// deadline is the run context's, read once at the top of run.
	deadline    time.Time
	hasDeadline bool
}

// shouldYield reports that the run is inside its yield margin (strictly less
// than yieldMargin left before the context's deadline) and that this attempt's
// batch 0 has completed, and latches it: once true it stays true. The batch
// loop asks it before starting a batch; the worker asks it (as giveUp) when a
// pause or retry sleep would otherwise go on, so no wait carries the run past
// the deadline. It is false without a margin or a deadline (HEAD).
func (l *fileIntroBatchLoop) shouldYield() bool {
	if l.yielding.Load() {
		return true
	}
	if l.yieldMargin <= 0 || !l.hasDeadline || !l.firstDone.Load() || time.Until(l.deadline) >= l.yieldMargin {
		return false
	}
	l.yielding.Store(true)
	return true
}

// run returns the number of batches in the contiguous completed prefix of
// l.batches, whether it stopped early at the margin, and the first real error.
//
// A batch past the margin returns nil WITHOUT running, rather than an error: a
// FailFast loop cancels the items still in flight on the first error, and a
// yield must let them finish (their results are written as they land). The
// price is that the loop counts a skipped batch as completed in its own
// progress; the caller resets the bar to the exact prefix.
//
// The margin is checked before a batch starts, and by the batch's own waits
// (the scan-busy pause before the whisper call and at the write gate, and the
// outage retry sleep): a wait that would go on inside the margin returns
// errFileIntroYieldNow, and the batch is skipped like one that never started
// (not done, not failed; results already answered but not yet written are
// dropped and re-sent by the next attempt, at most one batch of whisper work
// per worker).
//
// The whisper call is bounded too (review N16): it runs under a context that
// ends fileIntroCallMargin before the deadline (fileIntroWorker.boundCall), the
// dispatcher's pacing and the transport's queueing behind the book op included.
// A call cut that way returns errFileIntroYieldNow with nothing written for it;
// its files get no result and no strike and are re-sent by the next attempt.
//
// So a native run does not end timed_out because of a wait, with two exceptions
// that share one reason: a yield needs this attempt to have completed a batch
// (the executor fails a yield that made no progress past its starting point), so
// nothing is bounded until batch 0 is done, and a scan that outlasts the whole
// attempt, found by a resumed attempt before its batch 0, is not either. Such an
// attempt has made no progress for its entire timeout.
//
// "Inside the margin" means time.Until(deadline) < yieldMargin, strictly: with
// exactly yieldMargin left the run is "not yet within" it (time.Until(deadline)
// >= yieldMargin is the not-yet test shouldYield makes), and it yields only once
// this attempt's FIRST batch has completed: a yield must leave a prefix of at
// least one batch, or it would fail the executor's
// "progress past the starting point" proof and end the run. Waiting on batch 0
// rather than on any completed batch matters because the loop's workers start
// in no fixed order: a later batch can finish before batch 0 begins, and batch
// 0 would then start inside the margin and be skipped, leaving a prefix of
// zero. The cost is that batches keep starting past the margin while batch 0 is
// still running, and that a batch 0 still parked in a wait when the deadline
// arrives (it would have waited the whole attempt) is not given up: a yield
// with no completed batch would not count as progress either.
//
// A batch the call context cut still counts as not done, so the prefix, the
// cursor and the progress bar stay exact.
func (l *fileIntroBatchLoop) run(ctx context.Context) (prefix int, yielded bool, err error) {
	done := make([]bool, len(l.batches))
	var mu sync.Mutex
	advanced := 0 // length of the contiguous completed prefix, under mu
	l.deadline, l.hasDeadline = ctx.Deadline()
	if len(l.batches) > 0 {
		err = l.each(ctx, len(l.batches), t6g4EachOpts{
			// One worker per whisper slot across all endpoints. Each call carries one
			// batch; the dispatcher places it on an endpoint with a free slot,
			// rotating among equal-priority endpoints (allocateJobsWith), so the
			// workers spread over every Mac instead of queueing on the first. The
			// transport still enforces each endpoint's own cap.
			Concurrency:   l.slots,
			Reason:        "one worker per whisper slot across all endpoints (fileIntroSlots); the transport still enforces each endpoint's own cap",
			ProgressBase:  l.base,
			ProgressTotal: l.base + len(l.batches),
			Label:         l.label,
		}, func(ctx context.Context, i int) error {
			if l.shouldYield() {
				return nil
			}
			n, berr := l.runBatch(ctx, l.batches[i])
			if errors.Is(berr, errFileIntroYieldNow) {
				// A wait gave up inside the margin, or the whisper call was cut at
				// its own: the batch is skipped, exactly as one not started at the
				// margin (not done, not failed). The latch is not redundant: a cut
				// call does not go through shouldYield, so this is what makes run
				// report the yield.
				l.yielding.Store(true)
				return nil
			}
			l.afterBatch(n)
			if berr == nil {
				if i == 0 {
					l.firstDone.Store(true)
				}
				mu.Lock()
				done[i] = true
				grown := false
				for advanced < len(done) && done[advanced] {
					advanced++
					grown = true
				}
				if grown && l.onAdvance != nil {
					l.onAdvance(advanced)
				}
				mu.Unlock()
			}
			return berr
		})
	}
	mu.Lock()
	defer mu.Unlock()
	for prefix < len(done) && done[prefix] {
		prefix++
	}
	return prefix, err == nil && l.yielding.Load(), err
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
	store       fileIntroWorkerReads
	writer      fileIntroWorkerWrites
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
	// giveUp, when set (the native run, with a yield margin), reports that the
	// run is inside its margin: the scan-busy pause and the outage retry sleep
	// then stop waiting and the batch is left for the next attempt. nil (HEAD)
	// waits as long as it always did.
	giveUp func() bool
	// callMargin and batchZeroDone bound the whisper call (see boundCall): zero
	// and nil (HEAD) never cut a call.
	callMargin    time.Duration
	batchZeroDone func() bool
}

// callBoundHit reports that a whisper call must not start (or go on): the run
// has a call margin, a deadline, this attempt's batch 0 is done, and strictly
// less than the margin is left.
func (w *fileIntroWorker) callBoundHit(ctx context.Context) bool {
	if w.callMargin <= 0 || w.batchZeroDone == nil || !w.batchZeroDone() {
		return false
	}
	dl, ok := ctx.Deadline()
	return ok && time.Until(dl) < w.callMargin
}

// boundCall returns the context one whisper call runs under: ctx itself when
// the run has no call margin or deadline, otherwise ctx cancelled with
// errFileIntroYieldNow as its cause once callMargin is all that is left before
// the deadline AND this attempt's batch 0 is done (until then it re-asks every
// fileIntroPauseInterval, since a yield with no completed batch would not count
// as progress). The caller reads context.Cause before it calls the release.
func (w *fileIntroWorker) boundCall(ctx context.Context) (context.Context, context.CancelFunc) {
	dl, ok := ctx.Deadline()
	if w.callMargin <= 0 || w.batchZeroDone == nil || !ok {
		return ctx, func() {}
	}
	cctx, cancel := context.WithCancelCause(ctx)
	stop := make(chan struct{})
	go func() {
		for {
			wait := time.Until(dl.Add(-w.callMargin))
			if wait <= 0 {
				if w.batchZeroDone() {
					cancel(errFileIntroYieldNow)
					return
				}
				wait = fileIntroPauseInterval
			}
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-cctx.Done():
				t.Stop()
				return
			case <-stop:
				t.Stop()
				return
			}
		}
	}()
	return cctx, func() { close(stop); cancel(nil) }
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
	return waitWhileScanBusyUnless(ctx, opID, queue, onWait, nil)
}

// errFileIntroYieldNow is what a wait of the native transcribe-file-intros run
// returns when it would have to keep waiting but the run is inside its yield
// margin: the batch is left for the next attempt (not failed, not completed).
// HEAD never sees it, because HEAD passes no giveUp.
var errFileIntroYieldNow = errors.New("transcribe-file-intros: inside the yield margin; the batch is left for the next attempt")

// waitWhileScanBusyUnless is waitWhileScanBusy with an optional bound: when
// giveUp is non-nil and reports true while a scan or bulk apply is still
// running, the wait ends with errFileIntroYieldNow instead of continuing. It is
// asked only when the wait would otherwise go on, so a batch that finds no scan
// running is never dropped for being inside the margin. A nil giveUp is the
// unbounded wait HEAD always had.
func waitWhileScanBusyUnless(ctx context.Context, opID string, queue OpQueueReader, onWait func(def string), giveUp func() bool) error {
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
			if giveUp != nil && giveUp() {
				return errFileIntroYieldNow
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

// waitWhileBusy is the per-file op's scan gate (waitWhileScanBusyUnless with the
// worker's giveUp, nil for HEAD) announcing the pause in the op's progress line.
func (w *fileIntroWorker) waitWhileBusy(ctx context.Context) error {
	return waitWhileScanBusyUnless(ctx, fileIntroOpID, w.queue, func(def string) {
		w.progress(introLiveMarker + "paused — " + def + " is running; resumes when it finishes")
	}, w.giveUp)
}

// sleepOrYield waits d, ctx-aware. With no giveUp it is one timer (HEAD's wait,
// unchanged); with one it waits in fileIntroPauseInterval slices and returns
// errFileIntroYieldNow as soon as giveUp reports true, so a retry sleep never
// carries the run past its yield margin.
func (w *fileIntroWorker) sleepOrYield(ctx context.Context, d time.Duration) error {
	if w.giveUp == nil {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
	end := time.Now().Add(d)
	for {
		if w.giveUp() {
			return errFileIntroYieldNow
		}
		left := time.Until(end)
		if left <= 0 {
			return nil
		}
		t := time.NewTimer(min(left, fileIntroPauseInterval))
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
		// Inside the call margin no request is started: the pool could pace for
		// longer than is left (review N16). What was answered by earlier tries is
		// dropped with the rest and re-sent by the next attempt.
		if w.callBoundHit(ctx) {
			return nil, errFileIntroYieldNow
		}
		out, err := func() (map[string]fileIntroWhisperResult, error) {
			// A request can outlast the watchdog's progress budget (a chunk of
			// clips each allowed up to the worker's per-clip deadline), so say
			// the run is alive while it waits. Deferred, so the ticker stops even
			// if the call panics.
			defer tickWhileWaiting(whisperWaitTickInterval, func(elapsed time.Duration) {
				w.progress(introLiveMarker + fmt.Sprintf("waiting on whisper: %d files in flight for %s",
					len(pending), elapsed.Round(time.Second)))
			})()
			cctx, release := w.boundCall(ctx)
			defer release()
			out, err := fileIntroWhisperFn(cctx, pending, nil)
			if err != nil && ctx.Err() == nil && errors.Is(context.Cause(cctx), errFileIntroYieldNow) {
				// The call context ended at the margin while the run's did not:
				// nothing is settled, nothing is struck.
				return nil, errFileIntroYieldNow
			}
			return out, err
		}()
		if errors.Is(err, errFileIntroYieldNow) {
			return nil, err
		}
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
		if serr := w.sleepOrYield(ctx, wait); serr != nil {
			return nil, serr
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
		if errors.Is(err, errFileIntroYieldNow) {
			// Inside the yield margin with the pool unreachable: nothing is
			// written, the batch is re-sent by the next attempt.
			return 0, err
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
			// The sentinel is a silence verdict by construction.
			Parse: silenceParse(),
		}}
	default:
		// Two parses of the same text, with two different jobs. The identity
		// columns (title, author, narrator, translator, cover artist) and the
		// status come from the LEGACY grammar, exactly as they always have: guards,
		// the metadata search query and the apply gate read them, and
		// TestLegacyParseGolden pins that grammar. The classification stored beside
		// them (IntroParse) comes from the clip grammar, which maintenance.
		// reparse-file-intros applies to stored text, so a new row and a re-parsed
		// one carry the same classification.
		fields := transcribe.ParseAudiobookIntro(r.Text)
		status, kind := statusOK, "ok"
		if fields.Title == "" {
			status, kind = statusUnparsed, "unparsed"
		}
		text := r.Text
		parse := introParseFromClip(transcribe.ParseClip(r.Text))
		return groupResult{kind: kind, hasResult: true, fillable: true, res: database.BookFileIntroResult{
			Status: status, Transcript: &text, At: now, Done: true,
			Title: fields.Title, Author: fields.Author, Narrator: fields.Narrator,
			Translator: fields.Translator, CoverArtist: fields.CoverArtist,
			Meta:  &database.IntroMeta{Model: r.Model, Language: r.Language, ClipLengthSec: fileIntroClipSeconds},
			Parse: &parse,
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
		Parse: donorParse(d.IntroParse, text),
	}}
	gr.fillable = text != transcribe.SilenceSentinel && (status == statusOK || status == statusUnparsed)
	return gr
}

// donorParse is the classification a reused result carries. The donor's own parse
// describes the same text, so it is copied (never shared, so the stored values never
// alias) when it is at the current clip grammar version or newer (a newer parser's
// verdict is left for that parser to own, as reparseDecide leaves it). A donor with
// no parse (a row transcribed before the field existed) or with one from an older
// grammar is NOT copied: the new row would then be stored already out of date and
// the re-parse op would have to find and rewrite it, so the parse is derived from
// the text now, exactly as a fresh transcription derives it.
func donorParse(donor *database.IntroParse, text string) *database.IntroParse {
	if donor != nil && donor.Version >= transcribe.ClipParserVersion {
		return cloneIntroParse(donor)
	}
	p := introParseFromClip(transcribe.ParseClip(text))
	return &p
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
		wrote, err := w.writer.SetBookFileIntro(m.BookID, m.FileID, r)
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
	out, err := w.writer.RecordIntroTimeout(m.BookID, m.FileID, database.IntroTimeoutStrike{
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
		// The strike came from a machine that already struck the file, or from
		// one the store cannot tell apart from those (an unknown machine, or the
		// first known one after only unknown ones, which is recorded): one sick
		// machine on two nights is not two pieces of evidence about the audio.
		log.Info("transcribe-file-intros: transcribe_timeout from a machine that may already have struck the file, deferred without a second strike",
			"file_id", m.FileID, "strikes", out.Strikes, "worker", gr.worker, "identity_recorded", out.IdentityRecorded)
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
	_, err := w.writer.ModifyBook(bookID, func(cur *database.Book) error {
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
