// file: internal/plugins/maintenance/batch_children.go
// version: 0.1.0
// guid: 1abc3eca-e2d0-4aee-befc-7b813b18c851
// last-edited: 2026-11-22

package maintenance

import (
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The per-batch children of the three whole-library kinds and the parent runs
// that enqueue them (F5-18h). The sweep itself is batch_sweep.go. The native
// kinds extract-wav-clips, intro-migrate-single-file and transcribe-file-intros
// are the PARENTS (they hold no rows, ResumeContinue): they plan the books, enqueue one
// child per batchSweepSize of them and fold the children's results. Each child
// keeps the write set its kind always had (Effects) and narrows what the
// dispatcher holds to its batch (Subjects). The cores are the ones the HEAD v2
// shells run, unchanged for a run with no batch scope.

// The child kinds. They are registered with the parents (t6g4KindsUnchecked)
// and are not meant to be started by hand: a hand start with params the parent
// did not plan is refused by Subjects unless it names a batch.
const (
	extractWAVBatchChildKind   tasks.Kind = extractWAVClipsKind + ".batch"
	introMigrateBatchChildKind tasks.Kind = introMigrateSingleFileKind + ".batch"
	fileIntroBatchChildKind    tasks.Kind = fileIntroKind + ".batch"
)

// batchFileReads is the one read the parents make to name a batch's file rows.
type batchFileReads interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// batchBooksOf resolves ids to the rows each child may write: every file row of
// the book. A book whose files cannot be read is listed with none, so the child
// can write none of its rows (its core counts that book out of scope), where
// HEAD's loops would have skipped it too.
func batchBooksOf(r batchFileReads, ids []string) []batchBook {
	out := make([]batchBook, len(ids))
	for i, id := range ids {
		out[i] = batchBook{ID: id}
		files, err := r.GetBookFiles(id)
		if err != nil {
			continue
		}
		for _, f := range files {
			out[i].Files = append(out[i].Files, f.ID)
		}
		slices.Sort(out[i].Files)
	}
	return out
}

// --- extract-wav-clips ------------------------------------------------------

// extractWAVBatchParams is the child's params: the parent's skip_existing and
// the batch.
type extractWAVBatchParams struct {
	SkipExisting *bool `json:"skip_existing,omitempty"`
	batchScope
	t6g4ModeKeys
}

func mergeExtractWAV(acc *extractWAVResult, c extractWAVResult) {
	acc.Extracted += c.Extracted
	acc.Skipped += c.Skipped
	acc.Failed += c.Failed
	acc.HashFailed += c.HashFailed
	acc.HashMismatched += c.HashMismatched
	acc.ClipLinkFailed += c.ClipLinkFailed
	acc.Total += c.Total
	acc.OutOfScope += c.OutOfScope
}

// extractWAVBatchParent is the live run of the extract-wav-clips parent.
func (p *Plugin) extractWAVBatchParent(rc *tasks.RC, base t6g4Base, in extractWAVNativeParams) (extractWAVResult, error) {
	ids, err := base.Store.ListBookIDs()
	if err != nil {
		return extractWAVResult{}, fmt.Errorf("list book ids: %w", err)
	}
	runID := string(rc.RunID())
	return runBatchSweep(newBatchHost(rc, base, extractWAVBatchChildKind), batchPlan[extractWAVResult]{
		IDs: ids, Size: batchSweepSize,
		Books: func(ids []string) []batchBook { return batchBooksOf(base.Store, ids) },
		Params: func(batch int, books []batchBook) any {
			return extractWAVBatchParams{
				SkipExisting: in.SkipExisting,
				batchScope:   batchScope{ParentRunID: runID, Batch: batch, Books: books},
			}
		},
		Merge: mergeExtractWAV,
	})
}

// extractWAVBatchKind is one batch of extract-wav-clips.
func (p *Plugin) extractWAVBatchKind(deps t6g4Deps) tasks.Definition {
	return tasks.Task(extractWAVBatchChildKind, tasks.TaskSpec[extractWAVBatchParams, extractWAVResult]{
		Title: "Extract WAV clips for one batch of books",
		Help:  "One batch of extract-wav-clips, started by that run for up to 100 books: extracts the first 90 seconds of each book's first audio file and persists the file hash where the row has none. It holds only its batch's books and files, so a write to any other book is not held behind it. Not started by hand.",
		// The kind's own write set (see extractWAVClipsKind): the hash write-back,
		// the aggregate recompute it can reach, the clips and links on disk.
		Effects: tasks.Writes(t6g4Res(ops.ResBookFiles, ops.ResBooks, ops.ResMetadataCache, ops.ResIndexes, ops.ResFiles)...),
		// The dispatcher holds the batch's book and book_file rows and, whole-kind,
		// files: ffmpeg reads the source paths and writes under the library root,
		// so a mover of those paths must still wait, as it waited behind the whole
		// run before. metadata_cache and indexes are in Effects (the recompute's
		// ModifyBook writes them) but not held per batch: they carry no row of this
		// batch's that another writer could reach, the same trade internal/repairs
		// makes for its per-book fixes.
		Subjects:        batchSubjects(func(p extractWAVBatchParams) batchScope { return p.batchScope }, tasks.Res("books", string(ops.ResFiles))),
		Perms:           tasks.Permissions{Start: t6g4StartRequirement()},
		Resume:          tasks.ResumeFromZero,
		Priority:        tasks.PriorityLow,
		ConcurrencyKey:  tasks.Key("books.maintenance.extract-wav-clips.batch"),
		Timeout:         2 * time.Hour,
		ProgressTimeout: 10 * time.Minute,
		Ready:           []tasks.ReadySignal{t6g4SignalMemDB},
		Run: func(rc *tasks.RC, in extractWAVBatchParams) (extractWAVResult, error) {
			if err := t6g4RefuseModeKeys(rc.Kind(), in.t6g4ModeKeys); err != nil {
				return extractWAVResult{}, err
			}
			if err := t6g4RequireLive(rc); err != nil {
				return extractWAVResult{}, err
			}
			if err := in.batchScope.validate(); err != nil {
				return extractWAVResult{}, err
			}
			if err := serverdecode.Check("maintenance.extract-wav-clips"); err != nil {
				return extractWAVResult{}, err
			}
			base, err := t6g4NativeBase(rc, deps, true)
			if err != nil {
				return extractWAVResult{}, err
			}
			return p.runExtractWAVClipsCore(rc.Context(), extractWAVParams{SkipExisting: in.SkipExisting, batch: &in.batchScope}, extractWAVClipsEnv{t6g4Base: base})
		},
	})
}

// --- intro-migrate-single-file ------------------------------------------------

// introMigrateBatchParams is the child's params: the parent's overwrite and the
// batch. The parent's last_book_id is not carried: the batch is already past it.
type introMigrateBatchParams struct {
	Overwrite *bool `json:"overwrite,omitempty"`
	batchScope
	t6g4ModeKeys
}

func mergeIntroMigrate(acc *introMigrateResult, c introMigrateResult) {
	acc.TotalBooks += c.TotalBooks
	if acc.Counts == nil {
		acc.Counts = map[string]int{}
	}
	for k, n := range c.Counts {
		acc.Counts[k] += n
	}
}

// introMigrateBatchParent is the live run of the intro-migrate parent. last_book_id
// resumes past a book exactly as HEAD's core does: the planned books start after
// it when it is in the list, and at the first book when it is not.
func (p *Plugin) introMigrateBatchParent(rc *tasks.RC, base t6g4Base, in introMigrateNativeParams) (introMigrateResult, error) {
	ids, err := base.Store.ListBookIDs()
	if err != nil {
		return introMigrateResult{}, fmt.Errorf("list book ids: %w", err)
	}
	if in.LastBookID != "" {
		if i := slices.Index(ids, in.LastBookID); i >= 0 {
			ids = ids[i+1:]
		}
	}
	runID := string(rc.RunID())
	return runBatchSweep(newBatchHost(rc, base, introMigrateBatchChildKind), batchPlan[introMigrateResult]{
		IDs: ids, Size: batchSweepSize,
		Books: func(ids []string) []batchBook { return batchBooksOf(base.Store, ids) },
		Params: func(batch int, books []batchBook) any {
			return introMigrateBatchParams{
				Overwrite:  in.Overwrite,
				batchScope: batchScope{ParentRunID: runID, Batch: batch, Books: books},
			}
		},
		Seed:  introMigrateResult{Counts: map[string]int{}},
		Merge: mergeIntroMigrate,
	})
}

// introMigrateBatchKind is one batch of intro-migrate-single-file.
func (p *Plugin) introMigrateBatchKind(deps t6g4Deps) tasks.Definition {
	return tasks.Task(introMigrateBatchChildKind, tasks.TaskSpec[introMigrateBatchParams, introMigrateResult]{
		Title: "Migrate intro transcripts for one batch of books",
		Help:  "One batch of intro-migrate-single-file, started by that run for up to 100 books: copies each single-file book's intro transcript onto its one file row. It holds only its batch's books and files, so a write to any other book is not held behind it. Not started by hand.",
		// The kind's own write set (see introMigrateSingleFileKind).
		Effects:         tasks.Writes(t6g4Res(ops.ResBookFiles, ops.ResBooks, ops.ResMetadataCache, ops.ResIndexes)...),
		Subjects:        batchSubjects(func(p introMigrateBatchParams) batchScope { return p.batchScope }),
		Perms:           tasks.Permissions{Start: t6g4StartRequirement()},
		Resume:          tasks.ResumeFromZero,
		Priority:        tasks.PriorityLow,
		ConcurrencyKey:  tasks.Key("books.maintenance.intro-migrate-single-file.batch"),
		Timeout:         30 * time.Minute,
		ProgressTimeout: 5 * time.Minute,
		Ready:           []tasks.ReadySignal{t6g4SignalMemDB},
		Run: func(rc *tasks.RC, in introMigrateBatchParams) (introMigrateResult, error) {
			if err := t6g4RefuseModeKeys(rc.Kind(), in.t6g4ModeKeys); err != nil {
				return introMigrateResult{}, err
			}
			if err := t6g4RequireLive(rc); err != nil {
				return introMigrateResult{}, err
			}
			if err := in.batchScope.validate(); err != nil {
				return introMigrateResult{}, err
			}
			base, err := t6g4NativeBase(rc, deps, true)
			if err != nil {
				return introMigrateResult{}, err
			}
			return p.runIntroMigrateSingleFileCore(rc.Context(), introMigrateParams{Overwrite: in.Overwrite, batch: &in.batchScope}, introMigrateEnv{t6g4Base: base})
		},
	})
}

// --- transcribe-file-intros ---------------------------------------------------

// fileIntroBatchParams is the child's params: the parent's flags and the batch.
// ClearRunID makes the child a rollback of that run's results in the batch.
type fileIntroBatchParams struct {
	OnlyMissing *bool  `json:"only_missing,omitempty"`
	RetryFailed *bool  `json:"retry_failed,omitempty"`
	ClearRunID  string `json:"clear_run_id,omitempty"`
	batchScope
	t6g4ModeKeys
}

// mergeFileIntro folds one child's result into the parent's. The selection-wide
// numbers (Eligible, TranscribedAtStart, Selected, Skipped*) belong to the
// parent's own library-wide selection and are not touched: a child saw only its
// batch, so summing them would make the monitor's coverage describe the batches
// that ran. TranscribedNow moves by what the child added to its own start.
func mergeFileIntro(acc *fileIntroResult, c fileIntroResult) {
	if c.Cleared != nil {
		if acc.Cleared == nil {
			acc.Cleared = &fileIntroClearResult{RunID: c.Cleared.RunID}
		}
		acc.Cleared.Files += c.Cleared.Files
		acc.Cleared.BookReverts += c.Cleared.BookReverts
		acc.Cleared.Errors += c.Cleared.Errors
	}
	if c.Stats == nil || acc.Stats == nil {
		return
	}
	a, s := acc.Stats, c.Stats
	a.TranscribedNow += s.TranscribedNow - s.TranscribedAtStart
	a.Attempted += s.Attempted
	a.OK += s.OK
	a.Unparsed += s.Unparsed
	a.Silence += s.Silence
	a.WhisperError += s.WhisperError
	a.FFmpegError += s.FFmpegError
	a.SourceMissing += s.SourceMissing
	a.TranscribeTimeout += s.TranscribeTimeout
	a.Reused += s.Reused
	a.Deferred += s.Deferred
	a.Stale += s.Stale
	a.AlreadyHad += s.AlreadyHad
	a.WriteErrors += s.WriteErrors
	a.BookFills += s.BookFills
}

// fileIntroBatchParent is the run of the transcribe-file-intros parent. A
// preview, which reads and writes nothing, is the shared core's own preview;
// only a live run sweeps.
func (p *Plugin) fileIntroBatchParent(rc *tasks.RC, base t6g4Base, in fileIntroNativeParams) (fileIntroResult, error) {
	params := fileIntroParams{OnlyMissing: in.OnlyMissing, RetryFailed: in.RetryFailed, ClearRunID: in.ClearRunID}
	if rc.Mode() != tasks.ModeLive {
		return p.runFileIntroTranscribeCore(rc.Context(), params, fileIntroEnv{t6g4Base: base})
	}
	store := base.Store
	var queue OpQueueReader
	if store.HasOperationQueue() {
		queue = store
	}
	runID := string(rc.RunID())
	host := newBatchHost(rc, base, fileIntroBatchChildKind)
	childParams := func(batch int, books []batchBook) any {
		return fileIntroBatchParams{
			OnlyMissing: in.OnlyMissing, RetryFailed: in.RetryFailed, ClearRunID: in.ClearRunID,
			// The children stamp their results with this run's id, not their own.
			batchScope: batchScope{ParentRunID: runID, Batch: batch, Books: books},
		}
	}

	if !store.FileIntroWritesAvailable() {
		return fileIntroResult{}, fmt.Errorf("%s: the store does not support per-file intro writes", fileIntroOpID)
	}
	// A live run refuses to start while a scan or bulk apply is running; a
	// resumed attempt and every child wait instead (see runFileIntroTranscribeCore).
	if !rc.Resumed() {
		if err := fileIntroRefuseIfBusy(queue); err != nil {
			return fileIntroResult{}, err
		}
	}

	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return fileIntroResult{}, fmt.Errorf("%s: read book files: %w", fileIntroOpID, err)
	}

	if in.ClearRunID != "" {
		work := map[string][]string{}
		for i := range cores {
			if cores[i].IntroRunOpID == in.ClearRunID {
				work[cores[i].BookID] = append(work[cores[i].BookID], cores[i].ID)
			}
		}
		cores = nil
		seed := fileIntroResult{RunID: in.ClearRunID, Cleared: &fileIntroClearResult{RunID: in.ClearRunID}}
		return runBatchSweep(host, batchPlan[fileIntroResult]{
			IDs: batchWorkIDs(work), Size: batchSweepSize,
			Books:  func(ids []string) []batchBook { return booksOfWork(work, ids) },
			Params: childParams,
			Seed:   seed, Merge: mergeFileIntro,
		})
	}

	onlyMissing := in.OnlyMissing == nil || *in.OnlyMissing
	retryFailed := in.RetryFailed != nil && *in.RetryFailed
	sel := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: onlyMissing, retryFailed: retryFailed})
	cores = nil
	slots := fileIntroSlots()
	if slots < 1 {
		return fileIntroResult{}, fmt.Errorf("%s: no whisper endpoint is configured (whisper_endpoints / whisper_remote_url); refusing to start a run that cannot transcribe", fileIntroOpID)
	}
	if err := fileIntroPreflight(rc.Context()); err != nil {
		return fileIntroResult{}, err
	}
	work := map[string][]string{}
	for _, g := range sel.groups {
		for _, m := range g.Members {
			work[m.BookID] = append(work[m.BookID], m.FileID)
		}
	}

	now := time.Now()
	seed := fileIntroResult{
		RunID: runID, Eligible: sel.eligible, Selected: sel.selected,
		Stats: &database.FileIntroStats{
			RunOpID: runID, StartedAt: now, UpdatedAt: now,
			Eligible: sel.eligible, TranscribedAtStart: sel.transcribedAtStart, TranscribedNow: sel.transcribedAtStart,
			Selected:      sel.selected,
			SkippedNoHash: sel.skippedNoHash, SkippedMissing: sel.skippedMissing,
			SkippedUnsupported: sel.skippedUnsupported, SkippedFailed: sel.skippedFailed,
		},
	}
	log := base.Logger()
	base.Log(slog.LevelInfo, "transcribe-file-intros: starting a batch sweep",
		slog.String("run_id", runID), slog.Int("books", len(work)), slog.Int("selected", sel.selected), slog.Int("eligible", sel.eligible))
	// The parent owns the stats key: the children persist nothing (their sink is
	// nil), so a monitor reads one run's counters, rolled up, not whichever child
	// wrote last.
	flush := func(r fileIntroResult, done bool) {
		if r.Stats == nil || base.Stats == nil {
			return
		}
		cp := *r.Stats
		cp.UpdatedAt, cp.Done = time.Now(), done
		if err := base.Stats.PutFileIntroStats(&cp); err != nil {
			log.Warn("transcribe-file-intros: stats write failed", "err", err)
		}
	}
	flush(seed, false)
	return runBatchSweep(host, batchPlan[fileIntroResult]{
		IDs: batchWorkIDs(work), Size: batchSweepSize,
		Books:  func(ids []string) []batchBook { return booksOfWork(work, ids) },
		Params: childParams,
		Seed:   seed, Merge: mergeFileIntro, Flush: flush,
	})
}

func batchWorkIDs(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// booksOfWork names each book with the files the parent selected in it.
func booksOfWork(work map[string][]string, ids []string) []batchBook {
	out := make([]batchBook, len(ids))
	for i, id := range ids {
		files := slices.Clone(work[id])
		slices.Sort(files)
		out[i] = batchBook{ID: id, Files: files}
	}
	return out
}

// fileIntroBatchKind is one batch of transcribe-file-intros (or of a rollback of
// one run, with clear_run_id).
func (p *Plugin) fileIntroBatchKind(deps t6g4Deps) tasks.Definition {
	return tasks.Task(fileIntroBatchChildKind, tasks.TaskSpec[fileIntroBatchParams, fileIntroResult]{
		Title: "Transcribe file intros for one batch of books",
		Help:  "One batch of transcribe-file-intros, started by that run for up to 100 books: transcribes the batch's selected files and stores each result on the file's own record, and fills a book's transcript only when it is empty. With clear_run_id it removes what that run wrote in the batch instead. It holds only its batch's books and files, so a write to any other book is not held behind it. Not started by hand.",
		// The kind's own write set (see fileIntroTranscribeKind): a file's intro
		// group (book_files), a book's transcript fill (ModifyBook: books,
		// metadata_cache, indexes). The counters (stats:transcribe-files) are the
		// parent's; the child persists none.
		Effects:         tasks.Writes(t6g4Res(ops.ResBookFiles, ops.ResBooks, ops.ResMetadataCache, ops.ResIndexes)...),
		Subjects:        batchSubjects(func(p fileIntroBatchParams) batchScope { return p.batchScope }),
		Perms:           tasks.Permissions{Start: t6g4StartRequirement()},
		Resume:          tasks.ResumeFromZero,
		Priority:        tasks.PriorityLow,
		ConcurrencyKey:  tasks.Key("books." + fileIntroOpID + ".batch"),
		Timeout:         tasks.MaxTimeout,
		ProgressTimeout: 30 * time.Minute,
		Ready:           []tasks.ReadySignal{t6g4SignalMemDB},
		Run: func(rc *tasks.RC, in fileIntroBatchParams) (fileIntroResult, error) {
			if err := t6g4RefuseModeKeys(rc.Kind(), in.t6g4ModeKeys); err != nil {
				return fileIntroResult{}, err
			}
			if err := t6g4RequireLive(rc); err != nil {
				return fileIntroResult{}, err
			}
			if err := in.batchScope.validate(); err != nil {
				return fileIntroResult{}, err
			}
			base, err := t6g4NativeBase(rc, deps, true)
			if err != nil {
				return fileIntroResult{}, err
			}
			// A batch is minutes of work against a 24h attempt: no cursor of its own
			// (a restart re-runs it, and only_missing skips what is done), no yield
			// and no cut whisper call. The parent persists the counters.
			base.Stats, base.SaveCheckpoint, base.LoadCheckpoint = nil, nil, nil
			return p.runFileIntroTranscribeCore(rc.Context(), fileIntroParams{
				OnlyMissing: in.OnlyMissing, RetryFailed: in.RetryFailed, ClearRunID: in.ClearRunID, batch: &in.batchScope,
			}, fileIntroEnv{t6g4Base: base})
		},
	})
}
