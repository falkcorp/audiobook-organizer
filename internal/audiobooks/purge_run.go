// file: internal/audiobooks/purge_run.go
// version: 1.0.0
// guid: 4c9d2f87-6b13-4e50-a8d4-1f7e3a0c9b62
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// PurgeOptions are the parameters of one purge pass (RunPurge).
type PurgeOptions struct {
	// DeleteFiles removes a purged book's own files from disk (it never
	// applies to a book that owns book_file rows: those are not purged).
	DeleteFiles bool
	// OlderThanDays limits the pass to books trashed at least that long ago.
	OlderThanDays *int
	// TrashedBefore binds the pass to the books a preview showed: a book
	// trashed (or, with no trash stamp, last written) after this instant is
	// not touched, whatever its state. A preview (DryRun) returns the instant
	// it used in PurgeResult.TrashedBefore and the dialog sends it back with
	// the purge, so a purge cannot take a book the screen the user approved
	// never counted: the trash keeps changing while a dialog is open (a merge
	// trashes its losers, someone deletes a book), and a purge deletes the
	// row, its tombstone and, with DeleteFiles, its file with no rollback.
	// The bound is checked again under the merge lock on the book as it is
	// then, so a book restored and trashed again after the preview (a new
	// stamp) is left alone. A DryRun with none uses the instant it starts.
	TrashedBefore *time.Time
	// Unattended says there is no preview in front of this pass, so no
	// TrashedBefore to hold it to: the nightly job, which purges what the
	// retention window says. A real pass with neither this nor TrashedBefore
	// is refused (ErrPurgePreviewRequired), so an interactive caller that
	// forgot the preview fails closed instead of purging whatever is in the
	// trash by then.
	Unattended bool
	// DryRun classifies every book exactly as the real pass would and changes
	// nothing: no row, tombstone, external ID or listening state is touched.
	DryRun bool
	// Progress, when set, is called with (books decided, books total) every
	// purgeProgressEvery books and at the end. It runs inside worker
	// goroutines, concurrently: it must be safe for that.
	Progress func(done, total int)
}

// purgeProgressEvery is how often (in books) RunPurge reports progress.
const purgeProgressEvery = 200

// purgeEnv is what a purge pass reads once and every worker then only reads.
type purgeEnv struct {
	// probe answers "does a user have listening state on this book" with the
	// users listed once per pass.
	probe *merge.UserStateProbe
	// undo answers "does an undo that can still run need this trashed book":
	// a combine, a maintenance operation's ledger, or an auto-merge. It
	// refreshes the parts it snapshots when one is journaled mid-pass.
	undo *undoHold
	// trashedBefore is PurgeOptions.TrashedBefore (the effective bound): the
	// pass re-checks it on the row it re-reads under the merge lock. nil: no
	// bound (the nightly pass).
	trashedBefore *time.Time
	// preview is the preview the pass is bound to (purgePreviewRecord): the pass
	// purges only the books it listed as purgeable and removes only the files it
	// listed. nil: no preview binds the pass (the nightly one, and a preview).
	preview *purgePreviewRecord
	// files collects the removals a delete_files pass decides on, to be made once
	// every book is decided (removeDeferredFiles). nil: not a delete_files pass.
	files *pendingFiles
	// root is the pass's guard on the library root (rootGuard): checked at the
	// pass's start and re-checked at most every rootGuardTTL, so a mount lost
	// mid-pass stops the decisions that follow it. Only RunPurge sets it, for a
	// real delete_files pass; the single-book paths leave it nil and decideFile
	// checks afresh for each.
	root *rootGuard
	// lockHeld is set by an interactive caller that already holds the merge
	// lock (merge.TryLockMergeRMWWithin, so it can answer ErrMergeBusy instead
	// of hanging behind a combine): purgeIfStateless and purgeCarryingState
	// then run under that hold and do not take the non-reentrant lock again.
	// RunPurge never sets it.
	lockHeld bool
}

// undoShells is the pass's shared, self-refreshing view of
// merge.UndoShellIndex. The index is a snapshot, and a combine can journal
// while a pass runs (the soft-deleted listing was read before it): a snapshot
// taken at the start would not hold that shell and the purge would delete it,
// making the combine un-revertable. So every Holds call first checks whether a
// journal was written since the snapshot (merge.UndoShellIndex.Stale) and, if
// so, rebuilds it. Called under the merge lock (every journal writer holds it
// across the write), the answer is exact. It fails CLOSED: when the rebuild
// cannot read the journals, Holds returns the error and the caller keeps the
// book, never "nothing is held".
type undoShells struct {
	mu    sync.Mutex
	idx   *merge.UndoShellIndex
	build func() (*merge.UndoShellIndex, error)
}

func (svc *AudiobookService) newUndoShells() (*undoShells, error) {
	u := &undoShells{build: svc.newUndoShellIndex}
	idx, err := u.build()
	if err != nil {
		return nil, err
	}
	u.idx = idx
	return u, nil
}

// Holds reports whether b is the retired shell of a combine whose undo can
// still run. A non-nil error means it could not be decided: keep the book.
func (u *undoShells) Holds(b *database.Book) (journalID string, held bool, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.idx.Stale() {
		idx, berr := u.build()
		if berr != nil {
			return "", false, fmt.Errorf("a combine journaled since this pass began and the journals cannot be re-read: %w", berr)
		}
		u.idx = idx
	}
	journalID, held = u.idx.Holds(b)
	return journalID, held, nil
}

// newPurgeEnv builds a pass's shared read state. Every read fails closed: a
// purge that cannot list users, read the undo records (combine journals,
// auto-merge journal) or use the operation ledger's by-book index
// (ErrUndoLedgerNotReady) does not run, because "cannot tell" must never read
// as "nothing is held".
func (svc *AudiobookService) newPurgeEnv() (purgeEnv, error) {
	probe, err := merge.NewUserStateProbe(svc.store)
	if err != nil {
		return purgeEnv{}, fmt.Errorf("cannot list users to check their listening state: %w", err)
	}
	undo, err := svc.newUndoHold()
	if err != nil {
		return purgeEnv{}, err
	}
	return purgeEnv{probe: probe, undo: undo}, nil
}

// newUndoShellIndex reads the combine journals once. It fails closed: a store
// that cannot read them cannot say which shells are held, and "cannot tell"
// must not read as "none are".
func (svc *AudiobookService) newUndoShellIndex() (*merge.UndoShellIndex, error) {
	kv, ok := any(svc.store).(interface {
		GetRaw(key string) ([]byte, error)
		ScanPrefix(prefix string) ([]database.KVPair, error)
	})
	if !ok {
		return nil, errors.New("the store cannot read combine journals, so shells whose undo is still possible cannot be told apart")
	}
	return merge.NewUndoShellIndex(kv)
}

// purgeGate is the purge's side-effect-free, lock-free classifier: the checks
// that decide a book is NOT purged whatever else is true of it. It reports the
// outcome and blocked=true when one applies. Both a real pass (purgeOne) and a
// preview (previewOne) call it, so a preview's counts cannot disagree with the
// run that follows it.
//
// Order matters for the counts: a book that owns book_file rows is counted as
// owning files even if it is also a held shell. Fail closed: an unreadable
// file list is not proof there are no rows.
func (svc *AudiobookService) purgeGate(book *database.Book, env purgeEnv) (purgeOutcome, bool) {
	// CheckBookOwnsNoFiles is the prediction of DeleteBook's refusal: it counts
	// retired rows too (GetBookFiles skips them, so a book holding only retired
	// rows passed a check made with it and was refused after the side effects).
	if oerr := database.CheckBookOwnsNoFiles(svc.store, book.ID, "purge"); oerr != nil {
		if errors.Is(oerr, database.ErrBookOwnsFiles) {
			return purgeOutcome{kind: purgeOwnsFiles, ownsFilesErr: oerr}, true
		}
		return purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf("%s: not purged: %v", book.ID, oerr)}}, true
	}
	_, held, err := env.undo.Holds(book)
	switch {
	case err != nil:
		return purgeOutcome{kind: purgeFailed, abort: purgeAbortOf(err), errs: []string{fmt.Sprintf("%s: not purged: cannot tell whether an undo still needs it: %v", book.ID, err)}}, true
	case held:
		return purgeOutcome{kind: purgeKeptUndo}, true
	}
	return purgeOutcome{}, false
}

// previewOne is purgeOne without a side effect: the gate, then the listening-
// state check (which, as in the real pass, either carries to a listed sibling,
// or keeps the book). It answers what the real pass would do with this book
// right now.
func (svc *AudiobookService) previewOne(book *database.Book, env purgeEnv, deleteFiles bool) purgeOutcome {
	if o, blocked := svc.purgeGate(book, env); blocked {
		return o
	}
	has, err := env.probe.Has(book.ID)
	switch {
	case err != nil:
		return purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf("%s: would not be purged: %s: %v", book.ID, stateCheckFailureReason(err), err)}}
	case !has:
		return svc.previewPurged(book, purgePurged, deleteFiles)
	}
	sibling, err := svc.liveSiblingFor(book)
	switch {
	case err != nil:
		return purgeOutcome{kind: purgeCarryFailed, errs: []string{fmt.Sprintf("%s: would not be purged, listening state kept on it: cannot look for a live version to carry it to: %v", book.ID, err)}}
	case sibling == "":
		return purgeOutcome{kind: purgeKeptHasProgress}
	}
	return svc.previewPurged(book, purgeCarried, deleteFiles)
}

// previewPurged is the outcome of a book the preview says would be purged
// (kind purgePurged or purgeCarried), with what purgeFinish would then do about
// its file when the purge is asked to delete files. It is worked out whether or
// not the preview was asked for delete_files: the checkbox can change between
// the preview and the click, and the dialog shows the list only when it is on.
//
// A preview asked for delete_files counts a book whose file cannot be decided
// (fileUndecided) as failed, as the run does (decideFile): the book stays in the
// trash on that run, so the two must agree.
func (svc *AudiobookService) previewPurged(book *database.Book, kind purgeKind, deleteFiles bool) purgeOutcome {
	v := svc.planPurgeFile(book)
	if deleteFiles {
		if o := fileUndecided(book, v); o != nil {
			return *o
		}
	}
	return purgeOutcome{kind: kind, wouldRemove: v.remove, wouldKeepFile: v.protected || v.keptErr != nil}
}

// ErrPurgePreviewUnknown is what RunPurge refuses a bound pass with when no
// preview that returned its trashed_before is remembered: the server restarted
// since the preview, or the preview is older than the last few. The purge is
// held to what its preview listed, so without the preview it cannot run.
var ErrPurgePreviewUnknown = errors.New("purge refused: the preview that returned this trashed_before is no longer remembered (the server restarted, or newer previews replaced it), so what it listed cannot be checked; run the preview again")

// ErrPurgePreviewRequired is what RunPurge refuses a real, interactive pass
// with when it was not given the trashed_before of a preview (see
// PurgeOptions.TrashedBefore).
var ErrPurgePreviewRequired = errors.New("purge refused: it was not given the trashed_before of a preview, so nothing says which books the preview showed; run the preview first and send its trashed_before")

// RunPurge is one purge pass over the soft-deleted books (optionally only
// those trashed at least OlderThanDays ago), or, with DryRun, a preview of it.
// PurgeSoftDeletedUnattended (the nightly job) is RunPurge with the zero options
// besides its two parameters.
//
// What is purgeable is unchanged by this function: a book is purged only when
// it owns no book_file row, is not the shell of a combine whose undo can still
// run, and has no listening state it cannot carry to a live sibling. See
// purgeOne.
//
// The books are processed by a bounded worker pool, one version group per
// worker at a time (purgePartitions), so two workers never carry state into
// or delete from the same group concurrently. The carries, and each book's
// state check plus row delete, still run one at a time: each holds the
// global merge lock, which is what keeps a merge revert from putting state
// back on a book between its check and its delete. The file-row check and
// the on-disk file removal run outside it, in parallel.
func (svc *AudiobookService) RunPurge(ctx context.Context, opts PurgeOptions) (*PurgeResult, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// File removals an earlier pass decided on and never finished (it crashed,
	// or a restart stopped it) are carried out first, through the same checks a
	// pass in progress runs, but only by a pass that deletes files: with
	// "also delete files" off nothing is removed from disk, whatever an
	// earlier pass left. A resumed op keeps its saved delete_files, so a
	// crashed pass's resume still replays. They need no preview, so they run
	// before the refusals below: a resumed op that is refused for want of its
	// preview still frees the files its predecessor left
	// (purge_pending_journal.go). Their count and error lines are the result's
	// own fields, apart from this pass's (FilesReplayed; Errors, last).
	var replayed int
	var replayErrs []string
	// When the pass's own library-root check began: the guard that carries it to
	// the book decisions (rootGuard) trusts it no longer than rootGuardTTL after
	// THAT, not after the replay that follows it.
	var rootCheckedAt time.Time
	if !opts.DryRun && opts.DeleteFiles {
		// Every part of a delete_files pass reads "no file at this path" as "the
		// file is already gone", so it runs only with a library root that is
		// there (libraryRootReady). Refused whole, with every journal record kept:
		// the books stay in the trash, and the next pass tries again.
		rootCheckedAt = time.Now()
		if rootErr := svc.libraryRootReady(config.Snapshot().RootDir); rootErr != nil {
			return &PurgeResult{
				Errors:        []string{},
				StoppedEarly:  true,
				StopReason:    fmt.Sprintf("purge refused: %v", rootErr),
				TrashedBefore: opts.TrashedBefore,
			}, fmt.Errorf("purge refused: %w", rootErr)
		}
		replayed, replayErrs = svc.replayPendingFileRemovals(ctx)
		if replayed > 0 || len(replayErrs) > 0 {
			singleLog.Info("purge: replayed the file removals an earlier pass left pending: %d removed, %d error line(s)", replayed, len(replayErrs))
		}
	}

	if !opts.DryRun && opts.TrashedBefore == nil && !opts.Unattended {
		return &PurgeResult{
			Errors:        append([]string{}, replayErrs...),
			FilesReplayed: replayed,
			StoppedEarly:  true,
			StopReason:    ErrPurgePreviewRequired.Error(),
		}, ErrPurgePreviewRequired
	}

	// A pass that carries a preview's trashed_before is held to that preview:
	// the books it listed as purgeable and the files it listed. One that cannot
	// find the preview is refused before it reads anything.
	var preview *purgePreviewRecord
	if !opts.DryRun && !opts.Unattended {
		if preview = svc.previewFor(*opts.TrashedBefore); preview == nil {
			return &PurgeResult{
				Errors:        append([]string{}, replayErrs...),
				FilesReplayed: replayed,
				StoppedEarly:  true,
				StopReason:    ErrPurgePreviewUnknown.Error(),
				TrashedBefore: opts.TrashedBefore,
			}, ErrPurgePreviewUnknown
		}
	}

	started := svc.purgeNow()
	var cutoff *time.Time
	if opts.OlderThanDays != nil && *opts.OlderThanDays > 0 {
		ts := started.AddDate(0, 0, -*opts.OlderThanDays)
		cutoff = &ts
	}
	// bound: the instant the books this pass may touch were trashed by. A
	// purge takes the preview's; a preview takes the moment it starts, and
	// returns it. A caller-supplied bound after the pass began (a future
	// instant) is clamped to the start: it would otherwise let in books trashed
	// while the pass runs, which no preview could have shown. With a retention
	// window the bound is no later than the window's own cutoff, which the
	// preview's window put earlier than a later purge's: a book that only aged
	// into the window since the preview was not counted by it.
	var bound *time.Time
	switch {
	case opts.TrashedBefore != nil:
		b := *opts.TrashedBefore
		if b.After(started) {
			b = started
		}
		bound = &b
	case opts.DryRun:
		b := started
		bound = &b
	}
	if bound != nil && cutoff != nil && cutoff.Before(*bound) {
		bound = cutoff
	}

	// The whole trash, not the retention window's share of it: a book the
	// window leaves out still owns its file (settlePreviewFiles,
	// removeDeferredFiles), and the counts add up to the trash only if every
	// book is in them.
	all, err := svc.store.ListSoftDeletedBooks(purgeListLimit, 0, nil)
	if err != nil {
		return nil, err
	}
	books := make([]database.Book, 0, len(all))
	notDue, notShown := 0, 0
	for i := range all {
		switch {
		case cutoff != nil && !trashedAtOrBefore(&all[i], *cutoff):
			notDue++
		case bound != nil && !trashedAtOrBefore(&all[i], *bound):
			// After the bound. With a retention window and a preview, the bound
			// is the PREVIEW's window cutoff, which is earlier than this
			// pass's: a book that was already in the trash when the preview
			// ran and only aged into the window since is one the preview
			// counted as not yet due, and is again. Only a book trashed after
			// the preview began (or with no trash time) is "trashed after the
			// preview". This holds whatever window THIS purge was sent with:
			// the interval between the preview's bound and its start is empty
			// when the preview had no window, and a preview with one counted
			// every book in it as not yet due.
			if preview != nil && trashedAtOrBefore(&all[i], preview.started) {
				notDue++
			} else {
				notShown++
			}
		default:
			books = append(books, all[i])
		}
	}

	result := &PurgeResult{
		startedAt:           started,
		Attempted:           len(all),
		DryRun:              opts.DryRun,
		TrashedBefore:       bound,
		TrashedAfterPreview: notShown,
		NotYetDue:           notDue,
		FilesReplayed:       replayed,
		Errors:              []string{},
	}
	if opts.DryRun {
		// What a purge with delete_files would also do: the removals an earlier
		// pass left pending. Counted from the journal, which a preview never
		// changes.
		counts, cErr := svc.countPendingFileRemovals()
		if cErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("could not count the file removals an earlier pass left pending: %v", cErr))
		}
		result.FilesPendingFromEarlierPass = counts.Pending
		result.FilesWaitingForFolder = counts.WaitingForFolder
		// A purge with delete_files is refused while the root is not ready; say
		// so on the preview, where the owner decides whether to run it.
		status := &LibraryRootStatus{Ready: true, HungStats: int(rootStatsHung.Load())}
		if rootErr := svc.libraryRootReady(config.Snapshot().RootDir); rootErr != nil {
			status.Ready = false
			status.Problem = rootErr.Error()
			result.Errors = append(result.Errors, fmt.Sprintf("a purge with delete_files would be refused: %v", rootErr))
		}
		// Read again after the check: a check that just gave up has added its
		// hung stats.
		status.HungStats = int(rootStatsHung.Load())
		result.LibraryRoot = status
	}
	// Users, combine journals and the other undo records are read once per
	// pass, not once per book.
	var env purgeEnv
	if len(books) > 0 {
		env, err = svc.newPurgeEnv()
		if err != nil {
			// Nothing was decided. The result goes back with the error so the
			// operation can store it: a refusal is "did not run", and the
			// caller must be able to read why from the stored result as well
			// as from the failed operation.
			result.StoppedEarly = true
			result.StopReason = fmt.Sprintf("purge refused: %v", err)
			result.Errors = append(result.Errors, replayErrs...)
			return result, fmt.Errorf("purge refused: %w", err)
		}
		env.trashedBefore = bound
		env.preview = preview
		if !opts.DryRun && opts.DeleteFiles {
			env.files = &pendingFiles{preview: preview}
			env.root = newRootGuard(svc, config.Snapshot().RootDir, rootCheckedAt) // checked above, before the replay
		}
	}

	var mu sync.Mutex
	var decided atomic.Int64
	// The books the bound and the retention window left out are decided (not
	// touched) from the start.
	decided.Store(int64(notShown + notDue))
	total := len(all)
	// Progress is reported in order: the workers finish books concurrently,
	// so a bare "n := decided.Add(1)" lets a later worker's callback run
	// before an earlier one's and the figure goes backwards.
	var progressMu sync.Mutex
	lastReported := -1
	report := func(n int, final bool) {
		if opts.Progress == nil {
			return
		}
		progressMu.Lock()
		defer progressMu.Unlock()
		if n < lastReported || (n == lastReported && !final) {
			return
		}
		lastReported = n
		opts.Progress(n, total)
	}
	// A failure that repeats for every remaining book (the ledger index lost
	// its trust mid-pass) stops the pass with its reason, instead of failing
	// each remaining book one at a time.
	passCtx, stopPass := context.WithCancel(ctx)
	defer stopPass()
	var abortErr error
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for _, part := range purgePartitions(books) {
		if passCtx.Err() != nil {
			break
		}
		g.Go(func() error {
			for i := range part {
				if passCtx.Err() != nil {
					return nil
				}
				var out purgeOutcome
				if opts.DryRun {
					out = svc.previewOne(&part[i], env, opts.DeleteFiles)
				} else {
					out = svc.purgeOne(&part[i], opts.DeleteFiles, env)
				}
				mu.Lock()
				result.add(part[i].ID, out)
				if out.abort != nil && abortErr == nil {
					abortErr = out.abort
					stopPass()
				}
				mu.Unlock()
				if n := int(decided.Add(1)); n%purgeProgressEvery == 0 {
					report(n, false)
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		// The workers report per-book outcomes in result and return nil;
		// an error here is a bug, reported rather than dropped.
		result.Errors = append(result.Errors, fmt.Sprintf("purge worker failed: %v", err))
	}
	if abortErr != nil {
		// The pass-wide failure is the reason; the parent ctx may also be done.
		result.StoppedEarly = true
		result.StopReason = fmt.Sprintf("purge stopped: %v", abortErr)
	} else if err := ctx.Err(); err != nil {
		// A flag, not a line in Errors: Capped cuts Errors, and a stored result
		// that lost this line would read as a pass that finished.
		result.StoppedEarly = true
		result.StopReason = fmt.Sprintf("stopped early: %v", err)
	}
	// Every book is decided: now the files. A real pass removes what its books
	// named and nothing else names; a preview settles the same question for its
	// list. Both run even on a pass that stopped, because the rows they belong
	// to are already gone (or, for a preview, the counts say what was decided).
	if env.files != nil {
		// Not cancelled with the op: the rows these files belong to are already
		// gone, and finishing is what a cancelled pass did before. Serialized with
		// a replay (replayMu): both read and bump the same records.
		svc.replayMu.Lock()
		n, errs := svc.removeDeferredFiles(context.WithoutCancel(ctx), env.files, env.preview)
		svc.replayMu.Unlock()
		result.FilesDeleted += n
		result.Errors = append(result.Errors, errs...)
	}
	// The earlier pass's lines come after this pass's own: Capped cuts the tail
	// of Errors, and a pile of old lines must not push this pass's out.
	result.Errors = append(result.Errors, replayErrs...)
	if opts.DryRun && len(result.fileCands) > 0 {
		result.settlePreviewFiles(all)
	}
	result.sortIDs()
	report(int(decided.Load()), true)

	// A carry followed by a refused delete purges nothing but still moved a
	// sibling's progress, so it refreshes the cache like a purge does.
	if result.Purged > 0 || result.stateMoved > 0 {
		svc.InvalidateListCache()
	}
	svc.rememberPurge(result)
	if opts.DryRun {
		svc.rememberPreview(result)
	}
	return result, nil
}

// purgeNow is the moment a pass begins: time.Now, or the test clock.
func (svc *AudiobookService) purgeNow() time.Time {
	if svc.purgeClock != nil {
		return svc.purgeClock()
	}
	return time.Now()
}

// Summary is the one-line account of a pass, for the log and the activity
// entry: every outcome the pass counted.
func (r *PurgeResult) Summary() string {
	msg := r.summary()
	if r.StoppedEarly {
		return fmt.Sprintf("STOPPED EARLY (%s), the counts cover only the books decided before it stopped: %s", r.StopReason, msg)
	}
	return msg
}

func (r *PurgeResult) summary() string {
	// Left out of the bound: not decided, so it is said, not folded into a kept count.
	after := ""
	if r.NotYetDue > 0 {
		after += fmt.Sprintf("; %d not yet past the retention window (not touched)", r.NotYetDue)
	}
	if r.TrashedAfterPreview > 0 {
		after += fmt.Sprintf("; %d trashed after the preview (not touched)", r.TrashedAfterPreview)
	}
	if r.NotInPreview > 0 {
		after += fmt.Sprintf("; %d would be purged now but the preview had not listed them (not touched)", r.NotInPreview)
	}
	// Files an earlier pass decided to remove and left behind: not this pass's
	// books, so apart from FilesDeleted.
	if r.FilesPendingFromEarlierPass > 0 {
		after += fmt.Sprintf("; %d file(s) an earlier pass left waiting would also be removed by a purge with delete_files", r.FilesPendingFromEarlierPass)
	}
	// Not removed by any purge, with or without delete_files: only the owner can
	// settle them.
	if r.FilesWaitingForFolder > 0 {
		after += fmt.Sprintf("; %d file removal(s) from earlier passes are waiting for a folder that is missing or empty (a purge does not remove them; the records can be forgotten from the purge dialog)", r.FilesWaitingForFolder)
	}
	if r.FilesReplayed > 0 {
		after += fmt.Sprintf("; %d file(s) an earlier pass had decided to remove were removed too (not in the files deleted count)", r.FilesReplayed)
	}
	if r.DryRun {
		return fmt.Sprintf("Purge preview of %d soft-deleted books: %d purgeable now (%d of them would carry listening state to a sibling), %d kept: still own book_file rows, %d kept: an undo (combine, operation or auto-merge) can still run, %d kept: listening state with no listed sibling, %d could not be decided (carry_failed=%d, failed=%d); with delete_files it would remove %d file(s) and leave %d in place%s",
			r.Attempted, r.Purgeable, r.CarriedToSibling, r.SkippedOwnsFiles, r.KeptUndoPossible, r.KeptHasProgress, r.CarryFailed+r.Failed, r.CarryFailed, r.Failed, r.FilesToDeleteCount, r.FilesKept, after)
	}
	return fmt.Sprintf("Purged %d of %d soft-deleted books (%d files deleted); kept %d: still own book_file rows, %d: an undo (combine, operation or auto-merge) can still run, %d: listening state with no listed sibling; carried listening state to a sibling for %d; %d carry failed, %d failed%s",
		r.Purged, r.Attempted, r.FilesDeleted, r.SkippedOwnsFiles, r.KeptUndoPossible, r.KeptHasProgress, r.CarriedToSibling, r.CarryFailed, r.Failed, after)
}

// Decided is how many books the pass reached a decision on: every outcome the
// counts record. On a pass that stopped early it is below Attempted. A book
// that was restored or purged meanwhile counts in none of them, so on a very
// busy trash it can read a little low; it is a progress figure, not a ledger.
func (r *PurgeResult) Decided() int {
	n := r.Purged + r.Purgeable + r.SkippedOwnsFiles + r.KeptUndoPossible + r.KeptHasProgress + r.CarryFailed + r.Failed + r.TrashedAfterPreview + r.NotYetDue + r.NotInPreview
	// CarriedToSibling is also counted in Purged/Purgeable.
	if n > r.Attempted {
		return r.Attempted
	}
	return n
}

// Capped returns a copy of r with each ID list and Errors cut to at most n
// entries (Truncated set when anything was cut), so the result can be stored
// as an operation result: on prod the owns-files list alone is ~50k ids. The
// counts are exact either way.
func (r *PurgeResult) Capped(n int) *PurgeResult {
	c := *r
	cut := func(s []string) []string {
		if len(s) > n {
			c.Truncated = true
			return s[:n:n]
		}
		return s
	}
	c.SkippedOwnsFilesIDs = cut(r.SkippedOwnsFilesIDs)
	c.CarriedToSiblingIDs = cut(r.CarriedToSiblingIDs)
	c.KeptHasProgressIDs = cut(r.KeptHasProgressIDs)
	c.KeptUndoPossibleIDs = cut(r.KeptUndoPossibleIDs)
	c.CarryFailedIDs = cut(r.CarryFailedIDs)
	c.FilesToDelete = cut(r.FilesToDelete)
	c.Errors = cut(r.Errors)
	return &c
}

// PurgeBreakdown is the trash's purgeable-versus-blocked split as the last
// purge pass measured it. The trash listing shows it; it is not recomputed
// per request (classifying every trashed book reads each one's file rows).
type PurgeBreakdown struct {
	// ComputedAt is when the pass that produced this finished.
	ComputedAt time.Time `json:"computed_at"`
	// FromPreview is true when a dry run produced it, false when a real pass
	// did (the figures are then what REMAINED).
	FromPreview bool `json:"from_preview"`
	// Total is the number of books the pass looked at (or, after a real pass,
	// that remain in the trash).
	Total int `json:"total"`
	// Purgeable is how many a real pass would purge now.
	Purgeable int `json:"purgeable"`
	// Blocked, by reason.
	OwnsFiles         int `json:"owns_files"`
	UndoPossible      int `json:"undo_possible"`
	HasListeningState int `json:"has_listening_state"`
	Failed            int `json:"failed"`
}

// breakdownOf converts a pass's result into the listing's figures. After a real
// pass the books it purged are gone, so the figures describe what is left. The
// books that failed are counted in Failed and nowhere else: a failure is
// "could not be decided", not "purgeable", so Purgeable is 0 after a real
// pass (a preview, not a failed run, is what says what a next pass would
// purge).
func breakdownOf(r *PurgeResult, at time.Time) PurgeBreakdown {
	b := PurgeBreakdown{
		ComputedAt:        at,
		FromPreview:       r.DryRun,
		OwnsFiles:         r.SkippedOwnsFiles,
		UndoPossible:      r.KeptUndoPossible,
		HasListeningState: r.KeptHasProgress,
		Failed:            r.Failed + r.CarryFailed,
	}
	if r.DryRun {
		b.Total = r.Attempted
		b.Purgeable = r.Purgeable
	} else {
		b.Total = r.Attempted - r.Purged
		b.Purgeable = 0
	}
	return b
}

// rememberPurge records the pass's breakdown for LastPurgeBreakdown. A pass
// that stopped early (cancelled, timed out) is not remembered: its figures
// cover only part of the trash.
func (svc *AudiobookService) rememberPurge(r *PurgeResult) {
	if r.StoppedEarly {
		return
	}
	b := breakdownOf(r, time.Now().UTC())
	svc.purgeBreakdownMu.Lock()
	svc.purgeBreakdown = &b
	svc.purgeBreakdownMu.Unlock()
}

// LastPurgeBreakdown returns the most recent pass's breakdown, or nil when no
// pass has run since the server started.
func (svc *AudiobookService) LastPurgeBreakdown() *PurgeBreakdown {
	svc.purgeBreakdownMu.Lock()
	defer svc.purgeBreakdownMu.Unlock()
	if svc.purgeBreakdown == nil {
		return nil
	}
	b := *svc.purgeBreakdown
	return &b
}
