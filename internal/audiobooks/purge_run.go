// file: internal/audiobooks/purge_run.go
// version: 1.6.0
// guid: 4c9d2f87-6b13-4e50-a8d4-1f7e3a0c9b62
// last-edited: 2026-10-23

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
	ownedFiles, err := svc.store.GetBookFiles(book.ID)
	if err != nil {
		return purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf("%s: not purged: cannot read its book_file rows: %v", book.ID, err)}}, true
	}
	if len(ownedFiles) > 0 {
		return purgeOutcome{kind: purgeOwnsFiles}, true
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
func (svc *AudiobookService) previewOne(book *database.Book, env purgeEnv) purgeOutcome {
	if o, blocked := svc.purgeGate(book, env); blocked {
		return o
	}
	has, err := env.probe.Has(book.ID)
	switch {
	case err != nil:
		return purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf("%s: would not be purged: %s: %v", book.ID, stateCheckFailureReason(err), err)}}
	case !has:
		return svc.previewPurged(book, purgePurged)
	}
	sibling, err := svc.liveSiblingFor(book)
	switch {
	case err != nil:
		return purgeOutcome{kind: purgeCarryFailed, errs: []string{fmt.Sprintf("%s: would not be purged, listening state kept on it: cannot look for a live version to carry it to: %v", book.ID, err)}}
	case sibling == "":
		return purgeOutcome{kind: purgeKeptHasProgress}
	}
	return svc.previewPurged(book, purgeCarried)
}

// previewPurged is the outcome of a book the preview says would be purged
// (kind purgePurged or purgeCarried), with what purgeFinish would then do about
// its file when the purge is asked to delete files. It is worked out whether or
// not the preview was asked for delete_files: the checkbox can change between
// the preview and the click, and the dialog shows the list only when it is on.
func (svc *AudiobookService) previewPurged(book *database.Book, kind purgeKind) purgeOutcome {
	v := svc.planPurgeFile(book)
	return purgeOutcome{kind: kind, wouldRemove: v.remove, wouldKeepFile: v.protected || v.keptErr != nil}
}

// ErrPurgePreviewRequired is what RunPurge refuses a real, interactive pass
// with when it was not given the trashed_before of a preview (see
// PurgeOptions.TrashedBefore).
var ErrPurgePreviewRequired = errors.New("purge refused: it was not given the trashed_before of a preview, so nothing says which books the preview showed; run the preview first and send its trashed_before")

// trashStamp is the instant book was trashed: MarkedForDeletionAt, which every
// trash path is meant to set; for a row a path trashed without it, the last
// write to the row (UpdatedAt, set on every write), which trashing is. nil when
// neither is known: such a book cannot be shown to predate a bound.
func trashStamp(b *database.Book) *time.Time {
	if b.MarkedForDeletionAt != nil {
		return b.MarkedForDeletionAt
	}
	return b.UpdatedAt
}

// trashedByBound reports whether b was trashed at or before bound, the test a
// bounded pass applies to every book (the listing, and again under the merge
// lock on the re-read row). A book with no stamp is not provably older, so it
// is left out: the bound exists to keep a purge from taking what the preview
// did not show.
func trashedByBound(b *database.Book, bound time.Time) bool {
	at := trashStamp(b)
	return at != nil && !at.After(bound)
}

// RunPurge is one purge pass over the soft-deleted books (optionally only
// those trashed at least OlderThanDays ago), or, with DryRun, a preview of it.
// PurgeSoftDeletedBooks (the nightly job) is RunPurge with the zero options
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

	if !opts.DryRun && opts.TrashedBefore == nil && !opts.Unattended {
		return &PurgeResult{
			Errors:       []string{},
			StoppedEarly: true,
			StopReason:   ErrPurgePreviewRequired.Error(),
		}, ErrPurgePreviewRequired
	}

	started := time.Now()
	var cutoff *time.Time
	if opts.OlderThanDays != nil && *opts.OlderThanDays > 0 {
		ts := started.AddDate(0, 0, -*opts.OlderThanDays)
		cutoff = &ts
	}
	// bound: the instant the books this pass may touch were trashed by. A
	// purge takes the preview's; a preview takes the moment it starts, and
	// returns it. With a retention window the bound is no later than the
	// window's own cutoff, which the preview's window put earlier than a later
	// purge's: a book that only aged into the window since the preview was
	// not counted by it.
	var bound *time.Time
	switch {
	case opts.TrashedBefore != nil:
		b := *opts.TrashedBefore
		bound = &b
	case opts.DryRun:
		b := started
		bound = &b
	}
	if bound != nil && cutoff != nil && cutoff.Before(*bound) {
		bound = cutoff
	}

	listed, err := svc.store.ListSoftDeletedBooks(1_000_000, 0, cutoff)
	if err != nil {
		return nil, err
	}
	books := listed
	notShown := 0
	if bound != nil {
		books = make([]database.Book, 0, len(listed))
		for i := range listed {
			if trashedByBound(&listed[i], *bound) {
				books = append(books, listed[i])
			} else {
				notShown++
			}
		}
	}

	result := &PurgeResult{
		Attempted:           len(listed),
		DryRun:              opts.DryRun,
		TrashedBefore:       bound,
		TrashedAfterPreview: notShown,
		Errors:              []string{},
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
			return result, fmt.Errorf("purge refused: %w", err)
		}
		env.trashedBefore = bound
	}

	var mu sync.Mutex
	var decided atomic.Int64
	// The books the bound left out are decided (not touched) from the start.
	decided.Store(int64(notShown))
	total := len(listed)
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
					out = svc.previewOne(&part[i], env)
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
	result.sortIDs()
	report(int(decided.Load()), true)

	if result.Purged > 0 {
		svc.InvalidateListCache()
	}
	svc.rememberPurge(result)
	return result, nil
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
	if r.TrashedAfterPreview > 0 {
		after = fmt.Sprintf("; %d trashed after the preview (not touched)", r.TrashedAfterPreview)
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
	n := r.Purged + r.Purgeable + r.SkippedOwnsFiles + r.KeptUndoPossible + r.KeptHasProgress + r.CarryFailed + r.Failed + r.TrashedAfterPreview
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
